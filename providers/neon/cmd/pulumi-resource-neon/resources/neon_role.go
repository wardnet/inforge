package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/wardnet/inforge/internal/pgrole"
)

// NeonRoleArgs are the inputs for a scoped per-service database role (ADR-0025).
// The owner connection URI is the database owner's credential, used once to apply
// the role's GRANTs; it is a secret and is never echoed in errors.
type NeonRoleArgs struct {
	ProjectId          string `pulumi:"projectId"`
	BranchId           string `pulumi:"branchId"`
	Database           string `pulumi:"database"`
	RoleName           string `pulumi:"roleName"`
	Permission         string `pulumi:"permission"` // "ro" | "rw"
	OwnerConnectionURI string `pulumi:"ownerConnectionUri" provider:"secret"`
	ApiKey             string `pulumi:"apiKey" provider:"secret"`
}

// NeonRoleState is the full state after the role is created: the connection value
// fields a database grant publishes. URL is the role's full connection URI exactly
// as Neon returns it (already URL-encoded), so a grant template can compose a DSN
// with `{URL}` without re-encoding hazards; the discrete fields are the literal
// (decoded) values for clients that take them separately.
type NeonRoleState struct {
	NeonRoleArgs
	User     string `pulumi:"user"`
	Password string `pulumi:"password" provider:"secret"`
	Host     string `pulumi:"host"`
	Port     string `pulumi:"port"`
	DBName   string `pulumi:"dbName"`
	URL      string `pulumi:"url" provider:"secret"`
}

// NeonRole manages a scoped per-service Postgres role on an existing Neon branch +
// database. Create makes the role via the Neon API, then connects as the database
// owner over pgx (CGO-free) to apply the ro/rw GRANTs, and returns the role's
// connection value fields. Deleting the resource reassigns/drops the role's owned
// objects and privileges and removes the role.
type NeonRole struct{}

func (*NeonRole) Create(
	ctx context.Context, req infer.CreateRequest[NeonRoleArgs],
) (infer.CreateResponse[NeonRoleState], error) {
	inp := req.Inputs
	if req.DryRun {
		return infer.CreateResponse[NeonRoleState]{
			ID:     inp.ProjectId + "/" + inp.BranchId + "/" + inp.RoleName,
			Output: NeonRoleState{NeonRoleArgs: inp},
		}, nil
	}

	if err := ensureRole(ctx, inp.ApiKey, inp.ProjectId, inp.BranchId, inp.RoleName); err != nil {
		return infer.CreateResponse[NeonRoleState]{}, err
	}

	stmts, err := pgrole.GrantSQL(inp.Permission, inp.RoleName, inp.Database)
	if err != nil {
		return infer.CreateResponse[NeonRoleState]{}, err
	}
	if err := runAsOwner(ctx, inp.OwnerConnectionURI, stmts); err != nil {
		return infer.CreateResponse[NeonRoleState]{}, err
	}

	uri, err := getConnectionURI(ctx, inp.ApiKey, inp.ProjectId, inp.BranchId, inp.RoleName, inp.Database)
	if err != nil {
		return infer.CreateResponse[NeonRoleState]{}, err
	}
	fields, err := parseConnURI(uri)
	if err != nil {
		return infer.CreateResponse[NeonRoleState]{}, err
	}
	fields.NeonRoleArgs = inp

	return infer.CreateResponse[NeonRoleState]{
		ID:     inp.ProjectId + "/" + inp.BranchId + "/" + inp.RoleName,
		Output: fields,
	}, nil
}

func (*NeonRole) Read(
	ctx context.Context, req infer.ReadRequest[NeonRoleArgs, NeonRoleState],
) (infer.ReadResponse[NeonRoleArgs, NeonRoleState], error) {
	url := fmt.Sprintf("%s/projects/%s/branches/%s/roles/%s",
		neonAPIBase, req.Inputs.ProjectId, req.Inputs.BranchId, req.Inputs.RoleName)
	_, status, err := neonDo(ctx, http.MethodGet, url, req.Inputs.ApiKey, nil)
	if err != nil {
		return infer.ReadResponse[NeonRoleArgs, NeonRoleState]{}, err
	}
	if status == http.StatusNotFound {
		return infer.ReadResponse[NeonRoleArgs, NeonRoleState]{}, nil
	}
	if status < 200 || status >= 300 {
		return infer.ReadResponse[NeonRoleArgs, NeonRoleState]{},
			fmt.Errorf("neon: read role %q failed (HTTP %d)", req.Inputs.RoleName, status)
	}
	return infer.ReadResponse[NeonRoleArgs, NeonRoleState](req), nil
}

func (*NeonRole) Delete(
	ctx context.Context, req infer.DeleteRequest[NeonRoleState],
) (infer.DeleteResponse, error) {
	st := req.State
	// The role may own objects (rw grants CREATE on schema public). Reassign them to
	// the owner and drop its privileges so the API drop succeeds. This is
	// best-effort: a connection failure here (e.g. a suspended/cold Neon endpoint at
	// destroy time) must NOT wedge the destroy — we proceed to the control-plane API
	// drop, which is the authority and works even when compute is suspended, and
	// which surfaces a real error if the role still owns objects. The operator can
	// re-run destroy once the endpoint is warm if cleanup was needed.
	if owner, err := connURIUser(st.OwnerConnectionURI); err == nil {
		_ = runAsOwner(ctx, st.OwnerConnectionURI, pgrole.ReassignDropSQL(st.RoleName, owner))
	}

	url := fmt.Sprintf("%s/projects/%s/branches/%s/roles/%s",
		neonAPIBase, st.ProjectId, st.BranchId, st.RoleName)
	_, status, err := neonDo(ctx, http.MethodDelete, url, st.ApiKey, nil)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	if status != http.StatusNotFound && (status < 200 || status >= 300) {
		return infer.DeleteResponse{}, fmt.Errorf("neon: delete role %q failed (HTTP %d)", st.RoleName, status)
	}
	return infer.DeleteResponse{}, nil
}

// Diff customizes change detection so that drift in the owner connection URI (or
// the API key) does NOT churn the role. Those are transient capabilities used only
// to apply GRANTs at create time, not part of the role's identity — Neon
// connection URIs are not byte-stable (pooled vs direct endpoint, password reveal,
// host changes), and the default structural diff would otherwise replace EVERY
// per-service role under a database whenever the owner URI shifts. Identity fields
// (project/branch/database/roleName) and the permission still trigger a replace.
func (*NeonRole) Diff(_ context.Context, req infer.DiffRequest[NeonRoleArgs, NeonRoleState]) (infer.DiffResponse, error) {
	old, nw := req.State.NeonRoleArgs, req.Inputs
	diff := map[string]p.PropertyDiff{}
	if old.ProjectId != nw.ProjectId {
		diff["projectId"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if old.BranchId != nw.BranchId {
		diff["branchId"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if old.Database != nw.Database {
		diff["database"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if old.RoleName != nw.RoleName {
		diff["roleName"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	// A permission change re-applies privileges; with no Update method this is a
	// replace (drops + re-mints the role, rotating its password). Acceptable for a
	// short-lived per-service role — `inforge releases deploy` restarts the unit so
	// it re-fetches; documented in ADR-0025.
	if old.Permission != nw.Permission {
		diff["permission"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	// ownerConnectionUri and apiKey are deliberately ignored.
	return infer.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff}, nil
}

// runAsOwner opens a single pgx connection to connURI (the database owner's) and
// executes each statement in order. Errors never include connURI (it carries the
// owner password).
func runAsOwner(ctx context.Context, connURI string, stmts []string) error {
	conn, err := pgx.Connect(ctx, connURI)
	if err != nil {
		return fmt.Errorf("neon: connect as database owner: %w", redactURI(err, connURI))
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("neon: apply grant %q: %w", s, redactURI(err, connURI))
		}
	}
	return nil
}

// parseConnURI extracts the connection value fields from a Neon connection URI
// (postgresql://user:pass@host:port/db?...). Port defaults to 5432 when absent.
func parseConnURI(uri string) (NeonRoleState, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return NeonRoleState{}, redactParseErr("neon: parse connection URI", err)
	}
	pw, _ := u.User.Password()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	return NeonRoleState{
		User:     u.User.Username(),
		Password: pw,
		Host:     u.Hostname(),
		Port:     port,
		DBName:   strings.TrimPrefix(u.Path, "/"),
		// URL is the verbatim (already-encoded) connection URI, safe to drop into a
		// `{URL}` DSN template; the discrete fields above are the decoded values.
		URL: uri,
	}, nil
}

// connURIUser returns the username (database owner) from a connection URI.
func connURIUser(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", redactParseErr("neon: parse owner connection URI", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return "", fmt.Errorf("neon: owner connection URI has no username")
	}
	return u.User.Username(), nil
}

// redactParseErr wraps a url.Parse failure WITHOUT echoing the URL — a *url.Error's
// own Error() embeds the raw input verbatim (password and all), which would leak a
// role or owner credential into deploy logs. Keep the parse reason, drop the URL.
func redactParseErr(context string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %v (url redacted)", context, ue.Err)
	}
	return fmt.Errorf("%s: %w", context, err)
}

// redactURI replaces any occurrence of the secret connection URI in err with a
// placeholder, so a pgx error wrapping the DSN cannot leak the owner password.
func redactURI(err error, uri string) error {
	if err == nil || uri == "" {
		return err
	}
	if msg := err.Error(); strings.Contains(msg, uri) {
		return fmt.Errorf("%s", strings.ReplaceAll(msg, uri, "[redacted owner connection]"))
	}
	return err
}
