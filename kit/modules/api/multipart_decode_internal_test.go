package api

import (
	"bytes"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
)

func multipartBody(t *testing.T, parts func(w *multipart.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	parts(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Unmarshal reads a body held in memory the way the decoder reads a stream: plain and
// nested fields, files, and parts the message has no field for.
func TestMultipartUnmarshal(t *testing.T) {
	m := newMultipartMarshaler(&runtime.JSONPb{})
	if m.ContentType(nil) != "application/json" {
		t.Fatalf("content type = %s", m.ContentType(nil))
	}
	body := multipartBody(t, func(w *multipart.Writer) {
		_ = w.WriteField("title", "holiday")
		_ = w.WriteField("meta.author", "aidar")
		_ = w.WriteField("unknown", "ignored")
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="avatar"; filename="me.png"`)
		h.Set("Content-Type", "image/png")
		p, _ := w.CreatePart(h)
		_, _ = p.Write([]byte("png"))
		// A part without a name is skipped.
		anon, _ := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain"}})
		_, _ = anon.Write([]byte("nobody"))
		// A repeated file field with an indexed name.
		img := textproto.MIMEHeader{}
		img.Set("Content-Disposition", `form-data; name="images[1]"; filename="b.jpg"`)
		ip, _ := w.CreatePart(img)
		_, _ = ip.Write([]byte("jpg"))
		// A repeated message without a file is an empty slot.
		_ = w.WriteField("images", "")
	})
	var req testv1.UploadRequest
	if err := m.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req.GetTitle() != "holiday" || req.GetMeta().GetAuthor() != "aidar" ||
		req.GetAvatar().GetFilename() != "me.png" || string(req.GetAvatar().GetContent()) != "png" {
		t.Fatalf("req = %v", &req)
	}
	if n := len(req.GetImages()); n != 2 || req.GetImages()[1].GetFilename() != "b.jpg" {
		t.Fatalf("images = %v", req.GetImages())
	}
}

func TestMultipartRejectsBrokenBodies(t *testing.T) {
	m := newMultipartMarshaler(&runtime.JSONPb{})
	valid := multipartBody(t, func(w *multipart.Writer) { _ = w.WriteField("title", "x") })

	var notProto struct{}
	if err := m.Unmarshal(valid, &notProto); err == nil || !strings.Contains(err.Error(), "not a proto message") {
		t.Errorf("not a proto: %v", err)
	}
	for name, tc := range map[string]struct {
		body []byte
		want string
	}{
		"no boundary":      {[]byte("title=x"), "no boundary"},
		"cut short":        {[]byte("--b\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nx"), "multipart: read part"},
		"bad nested":       {multipartBody(t, func(w *multipart.Writer) { _ = w.WriteField("meta", "{not json") }), "multipart: field meta"},
		"through a scalar": {multipartBody(t, func(w *multipart.Writer) { _ = w.WriteField("meta.author.x", "1") }), ""}, // ignored, like an unknown field
	} {
		var req testv1.UploadRequest
		err := m.Unmarshal(tc.body, &req)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
