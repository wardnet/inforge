package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/r2"
)

// The Pulumi DIY (object-store) backend takes a lock object under
// .pulumi/locks/ for the duration of an update. A deploy that is CANCELLED or
// killed (a runner shutdown, a Ctrl-C at the wrong moment) never removes it —
// and every later `inforge deploy` then sits in the engine's lock-retry loop
// printing NOTHING (the retry messages ride the progress streams the CLI
// discards). That is a 30-minute "Deploying (prd): …silence" hang in CI.
//
// So the deploy does not enter the engine blind: it lists the stack's lock
// objects up front and FAILS FAST with the lock's identity and the fix
// (`inforge stack unlock <env>`). Only object-store backends have these lock
// objects; file/git-branch backends skip the check.

// stackLockChecker is the S3 surface the lock check and unlock need — an
// interface so tests exercise the filtering/formatting without AWS.
type stackLockChecker interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// lockObject is one Pulumi lock marker found in the state bucket.
type lockObject struct {
	Key      string
	Modified time.Time
}

// stateLockClient builds the S3 client + bucket for the project's state
// backend. ok=false when the backend has no lock objects to check (file /
// git-branch).
func stateLockClient(ctx context.Context, projCfg projectConfig) (stackLockChecker, string, bool, error) {
	switch projCfg.Backend.Type {
	case "r2":
		bucket := strings.TrimPrefix(projCfg.Backend.URL, "r2://")
		if bucket == "" || strings.Contains(bucket, "/") {
			return nil, "", false, fmt.Errorf("state backend: malformed r2 URL %q", projCfg.Backend.URL)
		}
		client, err := r2.NewClient(ctx, os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
		if err != nil {
			return nil, "", false, fmt.Errorf("state backend: %w", err)
		}
		return client, bucket, true, nil
	default:
		// s3 could be supported the same way (generic client); file/git-branch
		// have no shared lock objects. Skip rather than guess.
		return nil, "", false, nil
	}
}

// findStackLocks lists the state bucket's .pulumi/locks/ prefix and returns the
// lock objects belonging to stack. The path layout under locks/ is Pulumi's
// (org/project/stack segments whose exact shape has changed across versions),
// so membership is decided by path SEGMENT match on the stack name — never by
// substring — and the whole prefix is small (one object per in-flight update).
func findStackLocks(ctx context.Context, c stackLockChecker, bucket, stack string) ([]lockObject, error) {
	var out []lockObject
	var token *string
	for {
		resp, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(".pulumi/locks/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list state locks: %w", err)
		}
		for _, obj := range resp.Contents {
			key := aws.ToString(obj.Key)
			if slices.Contains(strings.Split(key, "/"), stack) {
				lo := lockObject{Key: key}
				if obj.LastModified != nil {
					lo.Modified = *obj.LastModified
				}
				out = append(out, lo)
			}
		}
		if resp.NextContinuationToken == nil {
			break
		}
		token = resp.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// failOnStackLock fails a deploy up front when the stack is locked, naming the
// lock and the fix. Without this the engine sits in its lock-retry loop
// emitting no events — and therefore no output — indefinitely.
func failOnStackLock(ctx context.Context, projCfg projectConfig, stack string) error {
	client, bucket, ok, err := stateLockClient(ctx, projCfg)
	if err != nil || !ok {
		return err
	}
	locks, err := findStackLocks(ctx, client, bucket, stack)
	if err != nil {
		// The check is advisory: a transient list failure must not block a deploy
		// the engine itself could still run.
		fmt.Fprintf(os.Stderr, "warning: could not check for state locks (%v) — continuing\n", err)
		return nil
	}
	if len(locks) == 0 {
		return nil
	}
	lines := make([]string, 0, len(locks))
	for _, l := range locks {
		age := ""
		if !l.Modified.IsZero() {
			age = fmt.Sprintf(" (created %s, %s ago)", l.Modified.UTC().Format(time.RFC3339), time.Since(l.Modified).Round(time.Minute))
		}
		lines = append(lines, "  "+l.Key+age)
	}
	return fmt.Errorf("stack %q is locked by a previous update:\n%s\n"+
		"A running deploy holds this lock legitimately — but a CANCELLED or killed deploy leaves it behind, "+
		"and every later deploy then waits on it silently. If no other deploy is running, clear it with:\n"+
		"  inforge stack unlock %s", stack, strings.Join(lines, "\n"), stack)
}

// newStackCmd is the `inforge stack` noun; `unlock` is its only verb today.
func newStackCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Operate on the Pulumi state backend for an environment",
	}
	cmd.AddCommand(newStackUnlockCmd(configPath))
	return cmd
}

func newStackUnlockCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "unlock <env>",
		Short:         "Remove stale state locks left by a cancelled or killed deploy",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			projCfg, err := loadProjectConfig(*configPath)
			if err != nil {
				return err
			}
			return runStackUnlock(cmd.Context(), projCfg, args[0])
		},
	}
}

func runStackUnlock(ctx context.Context, projCfg projectConfig, stack string) error {
	client, bucket, ok, err := stateLockClient(ctx, projCfg)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("stack unlock: backend type %q has no shared lock objects to clear", projCfg.Backend.Type)
	}
	locks, err := findStackLocks(ctx, client, bucket, stack)
	if err != nil {
		return err
	}
	if len(locks) == 0 {
		fmt.Printf("stack %q holds no locks — nothing to do\n", stack)
		return nil
	}
	for _, l := range locks {
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(l.Key),
		}); err != nil {
			return fmt.Errorf("delete lock %q: %w", l.Key, err)
		}
		fmt.Printf("removed %s\n", l.Key)
	}
	fmt.Printf("stack %q unlocked (%d lock(s) removed) — re-run the deploy\n", stack, len(locks))
	return nil
}
