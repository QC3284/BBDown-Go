package appapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// TestEncodePlayViewReqRoundTrip encodes a request and walks it back to
// verify the field numbers/values match the upstream playviewreq.proto.
func TestEncodePlayViewReqRoundTrip(t *testing.T) {
	b := encodePlayViewReq(170001, 279786, 0, code265)

	got := make(map[int]uint64)
	var spmid, fromSpmid string
	walkFields(b, func(fieldNum, wireType int, val []byte, varint uint64) bool {
		switch wireType {
		case 0:
			got[fieldNum] = varint
		case 2:
			if fieldNum == 9 {
				spmid = string(val)
			}
			if fieldNum == 10 {
				fromSpmid = string(val)
			}
		}
		return true
	})

	if got[1] != 170001 {
		t.Errorf("field 1 (epId/aid) = %d, want 170001", got[1])
	}
	if got[2] != 279786 {
		t.Errorf("field 2 (cid) = %d, want 279786", got[2])
	}
	if got[3] != maxAppQn {
		t.Errorf("field 3 (qn, 0 => %d) = %d", maxAppQn, got[3])
	}
	if got[5] != 4048 {
		t.Errorf("field 5 (fnval) = %d, want 4048", got[5])
	}
	if got[7] != 2 {
		t.Errorf("field 7 (forceHost) = %d, want 2", got[7])
	}
	if got[8] != 1 {
		t.Errorf("field 8 (fourk) = %d, want 1", got[8])
	}
	if got[12] != 2 {
		t.Errorf("field 12 (preferCodecType) = %d, want 2 (HEVC)", got[12])
	}
	if spmid != "main.ugc-video-detail.0.0" || fromSpmid != "main.my-history.0.0" {
		t.Errorf("spmid/fromSpmid = %q/%q", spmid, fromSpmid)
	}
}

func TestPackReadMessageRoundTrip(t *testing.T) {
	payload := []byte("hello playview")
	packed := packMessage(payload)
	if packed[0] != 1 {
		t.Fatal("packed flag byte should be 1 (gzip)")
	}
	got, err := readMessage(packed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip = %q", got)
	}
}

func TestGetVideoCodeType(t *testing.T) {
	cases := map[string]int{
		"AVC":  code264,
		"HEVC": code265,
		"AV1":  codeAV1,
		"":     code265,
	}
	for in, want := range cases {
		if got := getVideoCodeType(in); got != want {
			t.Errorf("getVideoCodeType(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestWalkFieldsRejectsMalformedInput guards the protobuf walker against
// attacker-controlled gRPC frames. A length-delimited field whose varint
// length exceeds MaxInt64 used to wrap negative in the int conversion, slip
// past the bounds check and slice out of range.
func TestWalkFieldsRejectsMalformedInput(t *testing.T) {
	noop := func(fieldNum, wireType int, val []byte, v uint64) bool { return true }

	// field 1, wire type 2, length = 0xFFFFFFFFFFFFFFFF
	overflowing := []byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}
	called := 0
	walkFields(overflowing, func(fieldNum, wireType int, val []byte, v uint64) bool {
		called++
		return true
	})
	if called != 0 {
		t.Errorf("overflowing length: fn called %d times, want 0", called)
	}

	// A varint field with no terminating byte must return, not spin forever.
	truncated := 0
	walkFields([]byte{0x08, 0x80, 0x80}, func(fieldNum, wireType int, val []byte, v uint64) bool {
		truncated++
		return true
	})
	if truncated != 0 {
		t.Errorf("truncated varint: fn called %d times, want 0", truncated)
	}

	// A well-formed field must still be delivered.
	ok := 0
	walkFields([]byte{0x0a, 0x02, 0xaa, 0xbb}, func(fieldNum, wireType int, val []byte, v uint64) bool {
		ok++
		if fieldNum != 1 || wireType != 2 || len(val) != 2 {
			t.Errorf("unexpected field: num=%d wire=%d val=%v", fieldNum, wireType, val)
		}
		return true
	})
	if ok != 1 {
		t.Errorf("well-formed field: fn called %d times, want 1", ok)
	}
	_ = noop
}

// ---- t70：gRPC 响应要同时看 grpc-status（吸收上游 1.7.4）----

// TestThrowIfGRPCStatusFails 钉住判定：HTTP 200 + grpc-status 非 0 时给出可读的
// 「权限/业务拒绝」错误（带状态码与人话原因、以及服务端 grpc-message），而不是让调用方
// 把它当成「protobuf 数据损坏」。
//
// 变异验证：撤掉 throwIfGRPCStatusFails 的调用 → 端到端用例红（本表用例仍绿，因为测的是纯函数）。
func TestThrowIfGRPCStatusFails(t *testing.T) {
	withTrailer := func(code, msg string) http.Header {
		h := http.Header{}
		h.Set("grpc-status", code)
		if msg != "" {
			h.Set("grpc-message", msg)
		}
		return h
	}

	// trailer 里的 7 = PERMISSION_DENIED：文案要含状态码、人话原因与服务端消息。
	err := throwIfGRPCStatusFails(http.Header{}, withTrailer("7", "PermissionDenied"))
	if err == nil {
		t.Fatal("grpc-status=7 应当报错")
	}
	for _, want := range []string{"grpc-status=7", "PERMISSION_DENIED", "权限", "PermissionDenied", "不是数据损坏"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案缺少 %q：%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "too short") {
		t.Errorf("不该退化成解析失败的错误：%v", err)
	}

	// 放在响应头里也要认（部分网关把 grpc-status 塞进 header）。
	if err := throwIfGRPCStatusFails(withTrailer("16", "unauthenticated"), nil); err == nil ||
		!strings.Contains(err.Error(), "UNAUTHENTICATED") {
		t.Errorf("header 里的 grpc-status 也要认：%v", err)
	}

	// trailer 优先于 header（规范位置是 trailer）。
	h := http.Header{}
	h.Set("grpc-status", "0")
	if err := throwIfGRPCStatusFails(h, withTrailer("7", "")); err == nil {
		t.Error("trailer 的 7 应当优先于 header 的 0")
	}

	// 0 / 缺失 / 非数字：一律不报（交回原来的错误路径）。
	if err := throwIfGRPCStatusFails(http.Header{}, withTrailer("0", "")); err != nil {
		t.Errorf("grpc-status=0 不该报错：%v", err)
	}
	if err := throwIfGRPCStatusFails(http.Header{}, http.Header{}); err != nil {
		t.Errorf("没有 grpc-status 时不该报错：%v", err)
	}
	if err := throwIfGRPCStatusFails(http.Header{}, withTrailer("abc", "")); err != nil {
		t.Errorf("非数字的 grpc-status 不该认（也不该报错）：%v", err)
	}
}

// newGRPCTestServer 起一个假 gRPC 服务：HTTP 200、空 body、grpc-status/message 按给定方式回。
// inTrailer=true 走 HTTP/2 风格 trailer（Go 的 resp.Trailer 读完 body 后填充），否则塞进响应头。
func newGRPCTestServer(t *testing.T, code, msg string, inTrailer bool) (*httptest.Server, func()) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		if inTrailer {
			w.Header().Set("Trailer", "grpc-status, grpc-message")
		}
		w.WriteHeader(http.StatusOK)
		if inTrailer {
			w.Header().Set("grpc-status", code)
			if msg != "" {
				w.Header().Set("grpc-message", msg)
			}
			return
		}
		w.Header().Set("grpc-status", code)
		if msg != "" {
			w.Header().Set("grpc-message", msg)
		}
	}))
	oldUGC, oldPGC := appUGCAPI, appPGCAPI
	appUGCAPI, appPGCAPI = srv.URL, srv.URL
	restore := func() {
		appUGCAPI, appPGCAPI = oldUGC, oldPGC
		srv.Close()
	}
	t.Cleanup(restore)
	return srv, restore
}

// TestDoReqReportsGRPCStatusOnHTTP200 是端到端用例：假 gRPC 服务返回 HTTP 200 + 空 body +
// trailer 里 grpc-status=7（权限拒绝）→ DoReq 的错误必须说「gRPC 调用被拒绝 / PERMISSION_DENIED」，
// **不能**是「gRPC response too short」（那是改前的误报）。
//
// 变异验证：把 DoReq 里的 throwIfGRPCStatusFails 调用撤掉 → 本用例红（错误变回 too short）。
func TestDoReqReportsGRPCStatusOnHTTP200(t *testing.T) {
	newGRPCTestServer(t, "7", "PermissionDenied", true)

	client := util.NewHTTPClient(func() bool { return true }, func() string { return "" }, nil)
	_, err := DoReq(context.Background(), client, "170001", "279786", "", "0", false, "AVC", "token")
	if err == nil {
		t.Fatal("HTTP 200 + grpc-status=7 必须报错")
	}
	msg := err.Error()
	for _, want := range []string{"grpc-status=7", "PERMISSION_DENIED", "权限", "PermissionDenied"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误文案缺少 %q：%v", want, err)
		}
	}
	if strings.Contains(msg, "too short") || strings.Contains(msg, "畸形") {
		t.Errorf("不能把权限拒绝误报成解析/帧错误：%v", err)
	}
}

// TestDoReqKeepsParseErrorWhenGRPCStatusZero 钉住「不误伤」：grpc-status=0（或没有）时，
// 解析失败仍然是**原来的**解析错误，不会被改写成 gRPC 拒绝。
func TestDoReqKeepsParseErrorWhenGRPCStatusZero(t *testing.T) {
	for _, inTrailer := range []bool{true, false} {
		newGRPCTestServer(t, "0", "", inTrailer)
		client := util.NewHTTPClient(func() bool { return true }, func() string { return "" }, nil)
		_, err := DoReq(context.Background(), client, "170001", "279786", "", "0", false, "AVC", "token")
		if err == nil {
			t.Fatalf("空 body（inTrailer=%v）应当解析失败", inTrailer)
		}
		if strings.Contains(err.Error(), "gRPC 调用被拒绝") {
			t.Errorf("grpc-status=0 时不该报 gRPC 拒绝：%v", err)
		}
		if !strings.Contains(err.Error(), "too short") {
			t.Errorf("应当保留原来的解析错误（too short）：%v", err)
		}
	}
}
