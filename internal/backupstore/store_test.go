package backupstore

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory s3API that paginates its listing in pages of pageSize
// so List's continuation-token loop is exercised.
type fakeS3 struct {
	objs     map[string]int64 // key -> size
	pageSize int
	base     time.Time
	calls    int
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.calls++
	prefix := aws.ToString(in.Prefix)
	// Deterministic key order: the store sorts the result anyway, but the fake
	// returns keys in sorted order so pagination slices are stable.
	var keys []string
	for k := range f.objs {
		if prefix == "" || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			keys = append(keys, k)
		}
	}
	// simple insertion sort — keeps the fake dependency-free
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	start := 0
	if in.ContinuationToken != nil {
		start, _ = strconv.Atoi(*in.ContinuationToken)
	}
	end := start + f.pageSize
	if end > len(keys) {
		end = len(keys)
	}
	var contents []s3types.Object
	for i := start; i < end; i++ {
		k := keys[i]
		sz := f.objs[k]
		contents = append(contents, s3types.Object{
			Key:          aws.String(k),
			Size:         aws.Int64(sz),
			LastModified: aws.Time(f.base.Add(time.Duration(i) * time.Second)),
		})
	}
	out := &s3.ListObjectsV2Output{Contents: contents}
	if end < len(keys) {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

func TestListPaginatesAndSorts(t *testing.T) {
	f := &fakeS3{
		pageSize: 2,
		base:     time.Unix(1_700_000_000, 0),
		objs: map[string]int64{
			"prd/use1/pg/app/20260101T000000Z.dump.gz": 10,
			"prd/use1/pg/app/20260102T000000Z.dump.gz": 20,
			"prd/use1/pg/app/20260103T000000Z.dump.gz": 30,
			"prd/use1/pg/app/20260104T000000Z.dump.gz": 40,
			"prd/use1/pg/app/20260105T000000Z.dump.gz": 50,
			// a different database under the env — must not leak into the prefix
			"prd/use1/pg/other/20260101T000000Z.dump.gz": 99,
		},
	}
	store := newStoreWithAPI(f, "backups")

	got, err := store.List(context.Background(), "prd/use1/pg/app/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 objects under the prefix, got %d: %+v", len(got), got)
	}
	// Three pages of size 2 (2+2+1) → the continuation loop ran ≥3 times.
	if f.calls < 3 {
		t.Fatalf("want ≥3 paginated calls, got %d", f.calls)
	}
	// Sorted by key → newest is last; timestamps sort chronologically.
	if want := "prd/use1/pg/app/20260105T000000Z.dump.gz"; got[len(got)-1].Key != want {
		t.Fatalf("newest = %q, want %q", got[len(got)-1].Key, want)
	}
	if got[0].Key != "prd/use1/pg/app/20260101T000000Z.dump.gz" {
		t.Fatalf("oldest = %q", got[0].Key)
	}
	if got[0].Size != 10 || got[4].Size != 50 {
		t.Fatalf("sizes not carried: %+v", got)
	}
}

func TestListEmptyPrefix(t *testing.T) {
	f := &fakeS3{pageSize: 10, base: time.Unix(1_700_000_000, 0), objs: map[string]int64{}}
	store := newStoreWithAPI(f, "backups")
	got, err := store.List(context.Background(), "prd/use1/pg/none/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no objects, got %d", len(got))
	}
}
