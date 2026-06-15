package resources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pulumi/pulumi-go-provider/infer"
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
// fields a database grant publishes.
type NeonRoleState struct {
	NeonRoleArgs
	User     string `pulumi:"user"`
	Password string `pulumi:"password" provider:"secret"`
	Host     string `pulumi:"host"`
	Port     string `pulumi:"port"`
	DBName   string `pulumi:"dbName"`
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

	stmts, err := grantSQL(inp.Permission, inp.RoleName, inp.Database)
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
	// the owner and drop its privileges before the API drop, which would otherwise
	// fail while the role still owns objects or holds grants.
	owner, err := connURIUser(st.OwnerConnectionURI)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	cleanup := []string{
		fmt.Sprintf(`REASSIGN OWNED BY %s TO %s`, quoteIdent(st.RoleName), quoteIdent(owner)),
		fmt.Sprintf(`DROP OWNED BY %s`, quoteIdent(st.RoleName)),
	}
	if err := runAsOwner(ctx, st.OwnerConnectionURI, cleanup); err != nil {
		// Tolerate a role that is already gone; surface anything else.
		if !strings.Contains(err.Error(), "does not exist") {
			return infer.DeleteResponse{}, err
		}
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

// grantSQL returns the ordered Postgres statements that grant the role its ro/rw
// privileges on schema public, run as the database owner. ALTER DEFAULT PRIVILEGES
// covers tables/sequences the owner creates later. rw additionally grants CREATE on
// the schema so the service can run its own migrations; ro is read-only.
func grantSQL(permission, role, database string) ([]string, error) {
	r := quoteIdent(role)
	db := quoteIdent(database)
	switch permission {
	case "ro":
		return []string{
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, r),
			fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON SEQUENCES TO %s`, r),
		}, nil
	case "rw":
		return []string{
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, r),
			fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s`, r),
		}, nil
	default:
		return nil, fmt.Errorf("neon: unknown grant permission %q (want ro or rw)", permission)
	}
}

// quoteIdent safely quotes a Postgres identifier (role/db/schema name).
func quoteIdent(ident string) string {
	return pgx.Identifier{ident}.Sanitize()
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
		return NeonRoleState{}, fmt.Errorf("neon: parse connection URI: %w", err)
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
	}, nil
}

// connURIUser returns the username (database owner) from a connection URI.
func connURIUser(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("neon: parse owner connection URI: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return "", fmt.Errorf("neon: owner connection URI has no username")
	}
	return u.User.Username(), nil
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
