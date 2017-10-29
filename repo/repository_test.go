package repo

import (
	"bytes"
	"compress/gzip"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"reflect"
	"runtime/debug"
	"testing"
	"time"

	"github.com/kopia/kopia/auth"
	"github.com/kopia/kopia/object"

	"github.com/kopia/kopia/internal/storagetesting"
	"github.com/kopia/kopia/storage"
)

func setupTest(t *testing.T, mods ...func(o *NewRepositoryOptions)) (map[string][]byte, *Repository) {
	return setupTestWithData(t, map[string][]byte{}, mods...)
}

func setupTestWithData(t *testing.T, data map[string][]byte, mods ...func(o *NewRepositoryOptions)) (map[string][]byte, *Repository) {
	st := storagetesting.NewMapStorage(data)

	creds, _ := auth.Password("foobarbazfoobarbaz")
	opt := &NewRepositoryOptions{
		MaxBlockSize:                200,
		Splitter:                    "FIXED",
		BlockFormat:                 "TESTONLY_MD5",
		MetadataEncryptionAlgorithm: "NONE",
		MaxPackedContentLength:      -1,

		noHMAC: true,
	}

	for _, m := range mods {
		m(opt)
	}
	Initialize(st, opt, creds)

	ctx := context.Background()

	r, err := connect(ctx, st, creds, &Options{
	//TraceStorage: log.Printf,
	})
	if err != nil {
		t.Fatalf("can't connect: %v", err)
	}

	return data, r
}

func TestWriters(t *testing.T) {
	cases := []struct {
		data     []byte
		objectID object.ID
	}{
		{
			[]byte("the quick brown fox jumps over the lazy dog"),
			object.ID{StorageBlock: "77add1d5f41223d5582fca736a5cb335"},
		},
		{make([]byte, 100), object.ID{StorageBlock: "6d0bb00954ceb7fbee436bb55a8397a9"}}, // 100 zero bytes
	}

	for _, c := range cases {
		data, repo := setupTest(t)

		writer := repo.Objects.NewWriter(object.WriterOptions{})

		writer.Write(c.data)

		result, err := writer.Result()
		if err != nil {
			t.Errorf("error getting writer results for %v, expected: %v", c.data, c.objectID.String())
			continue
		}

		repo.Objects.Flush()

		if !objectIDsEqual(result, c.objectID) {
			t.Errorf("incorrect result for %v, expected: %v got: %v", c.data, c.objectID.String(), result.String())
		}

		if got, want := len(data), 4; got != want {
			// 2 format blocks + 1 data block + 1 pack index block
			t.Errorf("unexpected data written to the storage (%v), wanted %v: %v", len(data), 3, data)
			dumpBlockManagerData(data)
		}
	}
}

func dumpBlockManagerData(data map[string][]byte) {
	for k, v := range data {
		if k[0] == 'P' {
			gz, _ := gzip.NewReader(bytes.NewReader(v))
			var buf bytes.Buffer
			buf.ReadFrom(gz)

			var dst bytes.Buffer
			json.Indent(&dst, buf.Bytes(), "", "  ")

			log.Printf("data[%v] = %v", k, dst.String())
		} else {
			log.Printf("data[%v] = %x", k, v)
		}
	}
}
func objectIDsEqual(o1 object.ID, o2 object.ID) bool {
	return reflect.DeepEqual(o1, o2)
}

func TestWriterCompleteChunkInTwoWrites(t *testing.T) {
	_, repo := setupTest(t)

	bytes := make([]byte, 100)
	writer := repo.Objects.NewWriter(object.WriterOptions{})
	writer.Write(bytes[0:50])
	writer.Write(bytes[0:50])
	result, err := writer.Result()
	if !objectIDsEqual(result, object.ID{StorageBlock: "6d0bb00954ceb7fbee436bb55a8397a9"}) {
		t.Errorf("unexpected result: %v err: %v", result, err)
	}
}

func TestPackingSimple(t *testing.T) {
	data, repo := setupTest(t, func(n *NewRepositoryOptions) {
		n.MaxPackedContentLength = 10000
	})

	content1 := "hello, how do you do?"
	content2 := "hi, how are you?"
	content3 := "thank you!"

	oid1a := writeObject(t, repo, []byte(content1), "packed-object-1a")
	oid1b := writeObject(t, repo, []byte(content1), "packed-object-1b")
	oid2a := writeObject(t, repo, []byte(content2), "packed-object-2a")
	oid2b := writeObject(t, repo, []byte(content2), "packed-object-2b")

	repo.Objects.Flush()

	oid3a := writeObject(t, repo, []byte(content3), "packed-object-3a")
	oid3b := writeObject(t, repo, []byte(content3), "packed-object-3b")
	verify(t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(t, repo, oid2a, []byte(content2), "packed-object-2")
	oid2c := writeObject(t, repo, []byte(content2), "packed-object-2c")
	oid1c := writeObject(t, repo, []byte(content1), "packed-object-1c")

	repo.Objects.Flush()

	if got, want := oid1a.String(), oid1b.String(); got != want {
		t.Errorf("oid1a(%q) != oid1b(%q)", got, want)
	}
	if got, want := oid1a.String(), oid1c.String(); got != want {
		t.Errorf("oid1a(%q) != oid1c(%q)", got, want)
	}
	if got, want := oid2a.String(), oid2b.String(); got != want {
		t.Errorf("oid2(%q)a != oidb(%q)", got, want)
	}
	if got, want := oid2a.String(), oid2c.String(); got != want {
		t.Errorf("oid2(%q)a != oidc(%q)", got, want)
	}
	if got, want := oid3a.String(), oid3b.String(); got != want {
		t.Errorf("oid3a(%q) != oid3b(%q)", got, want)
	}

	if got, want := len(data), 2+4; got != want {
		t.Errorf("got unexpected repository contents %v items, wanted %v", got, want)
		for k, v := range data {
			t.Logf("%v => %v", k, string(v))
		}
	}
	repo.Close()

	for k, v := range data {
		log.Printf("data[%v] = %v", k, string(v))
	}

	data, repo = setupTestWithData(t, data, func(n *NewRepositoryOptions) {
		n.MaxPackedContentLength = 10000
	})

	verify(t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(t, repo, oid2a, []byte(content2), "packed-object-2")
	verify(t, repo, oid3a, []byte(content3), "packed-object-3")

	if err := repo.Blocks.CompactIndexes(time.Now().Add(10*time.Second), nil); err != nil {
		t.Errorf("optimize error: %v", err)
	}
	data, repo = setupTestWithData(t, data, func(n *NewRepositoryOptions) {
		n.MaxPackedContentLength = 10000
	})

	verify(t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(t, repo, oid2a, []byte(content2), "packed-object-2")
	verify(t, repo, oid3a, []byte(content3), "packed-object-3")

	if err := repo.Blocks.CompactIndexes(time.Now().Add(-10*time.Second), nil); err != nil {
		t.Errorf("optimize error: %v", err)
	}
	data, repo = setupTestWithData(t, data, func(n *NewRepositoryOptions) {
		n.MaxPackedContentLength = 10000
	})

	verify(t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(t, repo, oid2a, []byte(content2), "packed-object-2")
	verify(t, repo, oid3a, []byte(content3), "packed-object-3")
}

func indirectionLevel(oid object.ID) int {
	if oid.Indirect == nil {
		return 0
	}

	return 1 + indirectionLevel(*oid.Indirect)
}

func TestHMAC(t *testing.T) {
	content := bytes.Repeat([]byte{0xcd}, 50)

	_, repo := setupTest(t)

	w := repo.Objects.NewWriter(object.WriterOptions{})
	w.Write(content)
	result, err := w.Result()
	if result.String() != "D999732b72ceff665b3f7608411db66a4" {
		t.Errorf("unexpected result: %v err: %v", result.String(), err)
	}
}

func TestReader(t *testing.T) {
	data, repo := setupTest(t)

	storedPayload := []byte("foo\nbar")
	data["a76999788386641a3ec798554f1fe7e6"] = storedPayload

	cases := []struct {
		text    string
		payload []byte
	}{
		{"Da76999788386641a3ec798554f1fe7e6", storedPayload},
	}

	for _, c := range cases {
		objectID, err := object.ParseID(c.text)
		if err != nil {
			t.Errorf("cannot parse object ID: %v", err)
			continue
		}

		reader, err := repo.Objects.Open(objectID)
		if err != nil {
			t.Errorf("cannot create reader for %v: %v", objectID, err)
			continue
		}

		d, err := ioutil.ReadAll(reader)
		if err != nil {
			t.Errorf("cannot read all data for %v: %v", objectID, err)
			continue
		}
		if !bytes.Equal(d, c.payload) {
			t.Errorf("incorrect payload for %v: expected: %v got: %v", objectID, c.payload, d)
			continue
		}

	}
}

func TestMalformedStoredData(t *testing.T) {
	data, repo := setupTest(t)

	cases := [][]byte{
		[]byte("foo\nba"),
		[]byte("foo\nbar1"),
	}

	for _, c := range cases {
		data["a76999788386641a3ec798554f1fe7e6"] = c
		objectID, err := object.ParseID("Da76999788386641a3ec798554f1fe7e6")
		if err != nil {
			t.Errorf("cannot parse object ID: %v", err)
			continue
		}

		reader, err := repo.Objects.Open(objectID)
		if err == nil || reader != nil {
			t.Errorf("expected error for %x", c)
		}
	}
}

func TestReaderStoredBlockNotFound(t *testing.T) {
	_, repo := setupTest(t)

	objectID, err := object.ParseID("Dno-such-block")
	if err != nil {
		t.Errorf("cannot parse object ID: %v", err)
	}
	reader, err := repo.Objects.Open(objectID)
	if err != storage.ErrBlockNotFound || reader != nil {
		t.Errorf("unexpected result: reader: %v err: %v", reader, err)
	}
}

func TestEndToEndReadAndSeek(t *testing.T) {
	_, repo := setupTest(t)

	for _, size := range []int{1, 199, 200, 201, 9999, 512434} {
		// Create some random data sample of the specified size.
		randomData := make([]byte, size)
		cryptorand.Read(randomData)

		writer := repo.Objects.NewWriter(object.WriterOptions{})
		writer.Write(randomData)
		objectID, err := writer.Result()
		writer.Close()
		if err != nil {
			t.Errorf("cannot get writer result for %v: %v", size, err)
			continue
		}

		verify(t, repo, objectID, randomData, fmt.Sprintf("%v %v", objectID, size))
	}
}

func writeObject(t *testing.T, repo *Repository, data []byte, testCaseID string) object.ID {
	w := repo.Objects.NewWriter(object.WriterOptions{})
	if _, err := w.Write(data); err != nil {
		t.Fatalf("can't write object %q - write failed: %v", testCaseID, err)

	}
	oid, err := w.Result()
	if err != nil {
		t.Fatalf("can't write object %q - result failed: %v", testCaseID, err)
	}

	return oid
}

func verify(t *testing.T, repo *Repository, objectID object.ID, expectedData []byte, testCaseID string) {
	t.Helper()
	reader, err := repo.Objects.Open(objectID)
	if err != nil {
		t.Errorf("cannot get reader for %v (%v): %v %v", testCaseID, objectID, err, string(debug.Stack()))
		return
	}

	for i := 0; i < 20; i++ {
		sampleSize := int(rand.Int31n(300))
		seekOffset := int(rand.Int31n(int32(len(expectedData))))
		if seekOffset+sampleSize > len(expectedData) {
			sampleSize = len(expectedData) - seekOffset
		}
		if sampleSize > 0 {
			got := make([]byte, sampleSize)
			if offset, err := reader.Seek(int64(seekOffset), 0); err != nil || offset != int64(seekOffset) {
				t.Errorf("seek error: %v offset=%v expected:%v", err, offset, seekOffset)
			}
			if n, err := reader.Read(got); err != nil || n != sampleSize {
				t.Errorf("invalid data: n=%v, expected=%v, err:%v", n, sampleSize, err)
			}

			expected := expectedData[seekOffset : seekOffset+sampleSize]

			if !bytes.Equal(expected, got) {
				t.Errorf("incorrect data read for %v: expected: %x, got: %x", testCaseID, expected, got)
			}
		}
	}
}

func TestFormats(t *testing.T) {
	makeFormat := func(objectFormat string) func(*NewRepositoryOptions) {
		return func(n *NewRepositoryOptions) {
			n.BlockFormat = objectFormat
			n.ObjectHMACSecret = []byte("key")
			n.MaxBlockSize = 10000
			n.Splitter = "FIXED"
			n.noHMAC = false
		}
	}

	cases := []struct {
		format func(*NewRepositoryOptions)
		oids   map[string]object.ID
	}{
		{
			format: func(n *NewRepositoryOptions) {
				n.MaxBlockSize = 10000
				n.noHMAC = true
			},
			oids: map[string]object.ID{
				"": object.ID{StorageBlock: "d41d8cd98f00b204e9800998ecf8427e"},
				"The quick brown fox jumps over the lazy dog": object.ID{
					StorageBlock: "9e107d9d372bb6826bd81d3542a419d6",
				},
			},
		},
		{
			format: makeFormat("UNENCRYPTED_HMAC_SHA256"),
			oids: map[string]object.ID{
				"The quick brown fox jumps over the lazy dog": object.ID{
					StorageBlock: "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8",
				},
			},
		},
		{
			format: makeFormat("UNENCRYPTED_HMAC_SHA256_128"),
			oids: map[string]object.ID{
				"The quick brown fox jumps over the lazy dog": object.ID{
					StorageBlock: "f7bc83f430538424b13298e6aa6fb143",
				},
			},
		},
	}

	for caseIndex, c := range cases {
		_, repo := setupTest(t, c.format)

		for k, v := range c.oids {
			bytesToWrite := []byte(k)
			w := repo.Objects.NewWriter(object.WriterOptions{})
			w.Write(bytesToWrite)
			oid, err := w.Result()
			if err != nil {
				t.Errorf("error: %v", err)
			}
			if !objectIDsEqual(oid, v) {
				t.Errorf("invalid oid for #%v\ngot:\n%#v\nexpected:\n%#v", caseIndex, oid.String(), v.String())
			}

			rc, err := repo.Objects.Open(oid)
			if err != nil {
				t.Errorf("open failed: %v", err)
				continue
			}
			bytesRead, err := ioutil.ReadAll(rc)
			if err != nil {
				t.Errorf("error reading: %v", err)
			}
			if !bytes.Equal(bytesRead, bytesToWrite) {
				t.Errorf("data mismatch, read:%x vs written:%v", bytesRead, bytesToWrite)
			}
		}
	}
}