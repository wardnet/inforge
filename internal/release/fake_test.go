package release

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeHTTPErr is an S3-shaped error carrying an HTTP status code, matching what
// isNotFound/isPreconditionFailed inspect (the real SDK transport error exposes
// the same HTTPStatusCode method).
type fakeHTTPErr struct {
	code int
	msg  string
}

func (e *fakeHTTPErr) Error() string       { return e.msg }
func (e *fakeHTTPErr) HTTPStatusCode() int { return e.code }

type fakeObj struct {
	body    []byte
	etag    string
	lastMod time.Time
}

// fakeS3 is an in-memory s3API. It honours If-Match / If-None-Match so the
// manifest compare-and-swap can be tested, and assigns monotonically increasing
// LastModified by write order so prune ordering is deterministic.
type fakeS3 struct {
	mu         sync.Mutex
	objs       map[string]*fakeObj
	seq        int
	base       time.Time
	putHook     func(key string)       // invoked at the start of each PutObject (race injection)
	deleteHook  func(key string) error // invoked at the start of each DeleteObject; a non-nil error fails that delete without removing the object (partial-prune-failure injection)
	putErrHook  func(key string) error // non-nil short-circuits PutObject with that error (non-404 failure injection)
	headErrHook func(key string) error // non-nil short-circuits HeadObject with that error (non-404 failure injection)
	getErrHook  func(key string) error // non-nil short-circuits GetObject with that error (non-404 failure injection)
	listErrHook func() error           // non-nil short-circuits ListObjectsV2 with that error
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objs: map[string]*fakeObj{}, base: time.Unix(1_700_000_000, 0)}
}

func (f *fakeS3) nextETag() string {
	f.seq++
	return fmt.Sprintf("\"etag-%d\"", f.seq)
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(in.Key)
	if f.putHook != nil {
		f.putHook(key)
	}
	if f.putErrHook != nil {
		if err := f.putErrHook(key); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	existing, exists := f.objs[key]
	if in.IfNoneMatch != nil && *in.IfNoneMatch == "*" && exists {
		return nil, &fakeHTTPErr{code: 412, msg: "precondition failed: object exists"}
	}
	if in.IfMatch != nil {
		if !exists || existing.etag != *in.IfMatch {
			return nil, &fakeHTTPErr{code: 412, msg: "precondition failed: etag mismatch"}
		}
	}
	var body []byte
	if in.Body != nil {
		b, err := io.ReadAll(in.Body)
		if err != nil {
			return nil, err
		}
		body = b
	}
	etag := f.nextETag()
	f.seq++
	f.objs[key] = &fakeObj{body: body, etag: etag, lastMod: f.base.Add(time.Duration(f.seq) * time.Second)}
	return &s3.PutObjectOutput{ETag: aws.String(etag)}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(in.Key)
	if f.getErrHook != nil {
		if err := f.getErrHook(key); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objs[key]
	if !ok {
		return nil, &fakeHTTPErr{code: 404, msg: "no such key"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.body)), ETag: aws.String(o.etag)}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	key := aws.ToString(in.Key)
	if f.headErrHook != nil {
		if err := f.headErrHook(key); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objs[key]; !ok {
		return nil, &fakeHTTPErr{code: 404, msg: "no such key"}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErrHook != nil {
		if err := f.listErrHook(); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := aws.ToString(in.Prefix)
	keys := make([]string, 0, len(f.objs))
	for k := range f.objs {
		if prefix == "" || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	contents := make([]s3types.Object, 0, len(keys))
	for _, k := range keys {
		lm := f.objs[k].lastMod
		contents = append(contents, s3types.Object{Key: aws.String(k), LastModified: aws.Time(lm)})
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	key := aws.ToString(in.Key)
	if f.deleteHook != nil {
		if err := f.deleteHook(key); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, key)
	return &s3.DeleteObjectOutput{}, nil
}
