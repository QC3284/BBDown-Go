package appapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// Bilibili APP playurl gRPC endpoint constants (upstream AppHelper).
const (
	apiUGC    = "https://grpc.biliapi.net/bilibili.app.playurl.v1.PlayURL/PlayView"
	apiPGC    = "https://app.bilibili.com/bilibili.pgc.gateway.player.v2.PlayURL/PlayView"
	dalvikVer = "2.1.0"
	osVer     = "11"
	brand     = "M2012K11AC"
	model     = "Build/RKQ1.200826.002"
	appVer    = "7.32.0"
	build     = 7320200
	channel   = "xiaomi_cn_tv.danmaku.bili_zm20200902"
	cronet    = "1.36.1"
	mobiApp   = "android"
	appKey    = "android64"
	sessionID = "dedf8669"
	platform  = "android"
	env       = "prod"
	appID     = 1
	region    = "CN"
	language  = "zh"
)

// CodeType enum values (PlayViewReq.CodeType).
const (
	code264 = 1
	code265 = 2
	codeAV1 = 3
)

// 端点接缝：变量而非常量，离线用例注入假 gRPC 服务（同 internal/fetcher 里那些接缝）；
// 生产路径就是上面两个常量。
var (
	appUGCAPI = apiUGC
	appPGCAPI = apiPGC
)

func getVideoCodeType(code string) int {
	switch code {
	case "AVC":
		return code264
	case "HEVC":
		return code265
	case "AV1":
		return codeAV1
	default:
		return code265
	}
}

// DoReq performs the APP playurl request and returns the web-style dash JSON
// (upstream AppHelper.DoReqAsync + ConvertToDashJson).
// grpcStatusReason 把常用的 gRPC 状态码翻译成一句人话（其余只给数字）。
func grpcStatusReason(code int) string {
	switch code {
	case 1:
		return "CANCELLED(请求被取消)"
	case 3:
		return "INVALID_ARGUMENT(参数不合法)"
	case 4:
		return "DEADLINE_EXCEEDED(超时)"
	case 5:
		return "NOT_FOUND(资源不存在)"
	case 7:
		return "PERMISSION_DENIED(权限拒绝)"
	case 8:
		return "RESOURCE_EXHAUSTED(配额/限流)"
	case 9:
		return "FAILED_PRECONDITION(前置条件不满足)"
	case 12:
		return "UNIMPLEMENTED(接口未实现)"
	case 13:
		return "INTERNAL(服务端内部错误)"
	case 14:
		return "UNAVAILABLE(服务不可用)"
	case 16:
		return "UNAUTHENTICATED(未认证/登录态无效)"
	default:
		return "未知状态"
	}
}

// grpcStatusFromHeaders 从响应头与 trailer 里取 (grpc-status, grpc-message)。
//
// 规范位置是 trailer（HTTP/2 trailers，Go 在读完 body 后填充 resp.Trailer），但有些网关会把
// 它放进响应头，所以两处都看：trailer 优先，header 兜底。
func grpcStatusFromHeaders(header, trailer http.Header) (int, string, bool) {
	get := func(h http.Header, key string) string {
		if h == nil {
			return ""
		}
		return strings.TrimSpace(h.Get(key))
	}
	for _, h := range []http.Header{trailer, header} {
		raw := get(h, "grpc-status")
		if raw == "" {
			continue
		}
		code, err := strconv.Atoi(raw)
		if err != nil {
			continue // 非数字：不认，按「没有 grpc-status」处理（不猜）
		}
		return code, get(h, "grpc-message"), true
	}
	return 0, "", false
}

// throwIfGRPCStatusFails 把「HTTP 200 + grpc-status 非 0」翻成可读错误（t70，上游 1.7.4）。
//
// 这类响应 body 往往是空的，直接交给 readMessage 只会得到「gRPC response too short」——
// 用户看到的是「数据损坏」，而真实原因是权限/登录/大会员被拒。
func throwIfGRPCStatusFails(header, trailer http.Header) error {
	code, message, ok := grpcStatusFromHeaders(header, trailer)
	if !ok || code == 0 {
		return nil
	}
	detail := "grpc-status=" + strconv.Itoa(code) + ": " + grpcStatusReason(code)
	if message != "" {
		detail += "（服务端消息: " + message + "）"
	}
	return fmt.Errorf("gRPC 调用被拒绝：%s。服务端返回了 HTTP 200，但业务层拒绝了这次请求"+
		"（常见原因：未登录/登录态失效、大会员或地区限制、接口参数不被接受）；这不是数据损坏", detail)
}

// postGRPC 发一次 gRPC 调用并返回 body + 响应头 + trailer。
//
// 为什么不用 client.PostResponse：它只回 body。这里用 client.DownloadClient() 的底层 client
// 自己发（请求本身是 token 鉴权、不带 cookie，也没有重定向，所以不踩那个 client 的凭据守卫）；
// 代价是没有内置超时，因此这里套一个 2 分钟上限（与 DRM 取钥窗口同量级），避免挂死。
func postGRPC(ctx context.Context, client *util.HTTPClient, endpoint string, body []byte, headers map[string]string) ([]byte, http.Header, http.Header, error) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(runCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/grpc")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	util.LogDebug("POST %s (%d bytes)", endpoint, len(body))

	resp, err := client.DownloadClient().Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.Header, resp.Trailer, fmt.Errorf("HTTP %d from %s", resp.StatusCode, util.MaskUrl(endpoint))
	}
	payload, err := util.ReadAllBounded(resp.Body, 8<<20)
	if err != nil {
		return nil, resp.Header, resp.Trailer, err
	}
	// 读完 body 之后 trailer 才到齐，这时取出来的才是完整的。
	return payload, resp.Header, resp.Trailer, nil
}

func DoReq(ctx context.Context, client *util.HTTPClient, aid, cid, epID, qn string, bangumi bool, encoding, token string) (string, error) {
	parseID := func(value, name string) (int64, error) {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s 必须是有效的数字 ID，当前值: %q", name, value)
		}
		return n, nil
	}

	var payload []byte
	var endpoint string
	if bangumi {
		if encoding != "" && encoding != "HEVC" {
			util.LogWarn("APP的番剧不支持 HEVC 以外的编码")
		}
		numEp, err := parseID(epID, "epID")
		if err != nil {
			return "", err
		}
		numCid, err := parseID(cid, "cid")
		if err != nil {
			return "", err
		}
		numQn, err := parseID(qn, "qn")
		if err != nil {
			return "", err
		}
		payload = encodePlayViewReq(numEp, numCid, numQn, code265)
		endpoint = appPGCAPI
	} else {
		numAid, err := parseID(aid, "aid")
		if err != nil {
			return "", err
		}
		numCid, err := parseID(cid, "cid")
		if err != nil {
			return "", err
		}
		numQn, err := parseID(qn, "qn")
		if err != nil {
			return "", err
		}
		payload = encodePlayViewReq(numAid, numCid, numQn, getVideoCodeType(encoding))
		endpoint = appUGCAPI
	}

	headers := getHeader(token)
	body := packMessage(payload)
	// t70（吸收上游 1.7.4）：这里不能用 client.PostResponse —— 它只回 body，把 header/trailer
	// 丢了；而 gRPC 的失败信息恰恰在 trailer 里（HTTP 200 + grpc-status 非 0）。自己发一次，
	// 把两个头都带回来。
	respBody, respHeader, respTrailer, err := postGRPC(ctx, client, endpoint, body, headers)
	if err != nil {
		return "", err
	}
	// gRPC 层拒绝优先于「解析失败」：HTTP 200 + grpc-status 非 0 是权限/业务拒绝，
	// 报成「protobuf 损坏」会把排查方向带偏（上游 1.7.4 修的正是这个）。
	if err := throwIfGRPCStatusFails(respHeader, respTrailer); err != nil {
		return "", err
	}
	msg, err := readMessage(respBody)
	if err != nil {
		// trailer 是**读完 body 之后**才到齐的（Go 的 resp.Trailer 在 ReadAll 后才填充），
		// 所以解析失败时再查一次，确保不会漏掉晚到的 grpc-status。
		if gerr := throwIfGRPCStatusFails(respHeader, respTrailer); gerr != nil {
			return "", gerr
		}
		return "", err
	}
	return convertToDashJSON(msg)
}

// maxAppQn 是 APP 协议请求的最高清晰度：129 = HDR Vivid（上游基线是 127；见 §4.41）。
// 提到常量是因为 127 → 129 曾撞掉两条写死字面量的用例。
const maxAppQn = 129

// encodePlayViewReq builds the PlayViewReq protobuf (fields upstream sets).
func encodePlayViewReq(id, cid, qn int64, codec int) []byte {
	var b []byte
	b = appendVarintField(b, 1, uint64(id))  // epId / aid
	b = appendVarintField(b, 2, uint64(cid)) // cid
	if qn == 0 {
		qn = maxAppQn
	}
	b = appendVarintField(b, 3, uint64(qn))                         // qn
	b = appendVarintField(b, 5, 4048)                               // fnval
	b = appendVarintField(b, 7, 2)                                  // forceHost = https
	b = appendVarintField(b, 8, 1)                                  // fourk
	b = appendBytesField(b, 9, []byte("main.ugc-video-detail.0.0")) // spmid
	b = appendBytesField(b, 10, []byte("main.my-history.0.0"))      // fromSpmid
	b = appendVarintField(b, 12, uint64(codec))                     // preferCodecType
	return b
}

// getHeader builds the gRPC binary headers (upstream GetHeader).
func getHeader(token string) map[string]string {
	ua := fmt.Sprintf("Dalvik/%s (Linux; U; Android %s; %s %s) %s os/android model/%s mobi_app/android build/%d channel/%s innerVer/%d osVer/%s network/2 grpc-java-cronet/%s", dalvikVer, osVer, brand, model, appVer, brand, build, channel, build, osVer, cronet)
	return map[string]string{
		"Host":                   "grpc.biliapi.net",
		"user-agent":             ua,
		"te":                     "trailers",
		"x-bili-fawkes-req-bin":  b64(fawkesReqBin()),
		"x-bili-metadata-bin":    b64(metadataBin(token)),
		"authorization":          "identify_v1 " + token,
		"x-bili-device-bin":      b64(deviceBin()),
		"x-bili-network-bin":     b64(networkBin()),
		"x-bili-restriction-bin": "",
		"x-bili-locale-bin":      b64(localeBin()),
		"x-bili-exps-bin":        "",
		"grpc-encoding":          "gzip",
		"grpc-accept-encoding":   "identity,gzip",
		"grpc-timeout":           "17996161u",
		"content-type":           "application/grpc",
	}
}

func fawkesReqBin() []byte {
	var b []byte
	b = appendBytesField(b, 1, []byte(appKey))
	b = appendBytesField(b, 2, []byte(env))
	b = appendBytesField(b, 3, []byte(sessionID))
	return b
}

func metadataBin(token string) []byte {
	var b []byte
	b = appendBytesField(b, 1, []byte(token))
	b = appendBytesField(b, 2, []byte(mobiApp))
	b = appendVarintField(b, 4, uint64(build))
	b = appendBytesField(b, 5, []byte(channel))
	b = appendBytesField(b, 6, []byte(""))
	b = appendBytesField(b, 7, []byte(platform))
	return b
}

func deviceBin() []byte {
	var b []byte
	b = appendVarintField(b, 1, uint64(appID))
	b = appendVarintField(b, 2, uint64(build))
	b = appendBytesField(b, 3, []byte(""))
	b = appendBytesField(b, 4, []byte(mobiApp))
	b = appendBytesField(b, 5, []byte(platform))
	b = appendBytesField(b, 7, []byte(channel))
	b = appendBytesField(b, 8, []byte(brand))
	b = appendBytesField(b, 9, []byte(model))
	b = appendBytesField(b, 10, []byte(osVer))
	return b
}

func networkBin() []byte {
	var b []byte
	b = appendVarintField(b, 1, 1) // WIFI
	b = appendBytesField(b, 3, []byte("46007"))
	return b
}

func localeBin() []byte {
	var ids []byte
	ids = appendBytesField(ids, 1, []byte(language))
	ids = appendBytesField(ids, 3, []byte(region))
	return appendBytesField(nil, 1, ids)
}

func b64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// packMessage frames + gzips the request body (upstream PackMessage).
func packMessage(input []byte) []byte {
	comp := gzipCompress(input)
	out := make([]byte, 5+len(comp))
	out[0] = 1
	binary.BigEndian.PutUint32(out[1:5], uint32(len(comp)))
	copy(out[5:], comp)
	return out
}

// readMessage deframes + ungzips the response (upstream ReadMessage).
func readMessage(data []byte) ([]byte, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("gRPC response too short: %d bytes", len(data))
	}
	first := data[0]
	size := int(binary.BigEndian.Uint32(data[1:5]))
	switch first {
	case 1:
		return gzipDecompress(data[5:])
	case 0:
		// Uncompressed: handled below.
	default:
		// The frame header's first byte is a compressed flag and may only be 0 or 1.
		// Anything else is a malformed frame; handing it to the protobuf decoder as
		// if it were payload yields confusing downstream errors (upstream validates).
		return nil, fmt.Errorf("畸形 gRPC 帧：首字节 %d（合法值仅 0/1）", first)
	}
	payloadLen := size
	if payloadLen > len(data)-5 {
		payloadLen = len(data) - 5
	}
	if payloadLen < 0 {
		return nil, fmt.Errorf("invalid gRPC payload length: %d", payloadLen)
	}
	if payloadLen < 0 {
		return nil, fmt.Errorf("invalid gRPC payload length: %d", payloadLen)
	}
	if payloadLen == 0 {
		// 空载荷是合法的（上游对 0 长度未压缩帧返回空数组）；
		// 只有 flag=gzip 且载荷为 0 才算畸形帧。
		return []byte{}, nil
	}
	return data[5 : 5+payloadLen], nil
}

func gzipCompress(data []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

// maxGRPCBodyBytes bounds the decompressed gRPC payload (upstream 48MB): a
// small gzip bomb would otherwise expand without limit.
const maxGRPCBodyBytes = 48 << 20

func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return util.ReadAllBounded(r, maxGRPCBodyBytes)
}

// ---- protobuf wire helpers ----

func appendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

func appendVarintField(buf []byte, fieldNum int, v uint64) []byte {
	buf = appendVarint(buf, uint64(fieldNum<<3))
	return appendVarint(buf, v)
}

func appendBytesField(buf []byte, fieldNum int, data []byte) []byte {
	buf = appendVarint(buf, uint64(fieldNum<<3|2))
	buf = appendVarint(buf, uint64(len(data)))
	return append(buf, data...)
}

func decodeVarint(data []byte) (uint64, int) {
	var result uint64
	var shift uint
	for i, b := range data {
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, i + 1
		}
		shift += 7
		if shift >= 64 {
			break
		}
	}
	return 0, 0
}

// walkFields iterates protobuf fields, calling fn per field.
func walkFields(data []byte, fn func(fieldNum, wireType int, val []byte, varint uint64) bool) {
	for pos := 0; pos < len(data); {
		key, n := decodeVarint(data[pos:])
		if n == 0 {
			break
		}
		pos += n
		fieldNum := int(key >> 3)
		wireType := int(key & 0x7)
		switch wireType {
		case 0:
			v, n := decodeVarint(data[pos:])
			if n == 0 {
				return
			}
			pos += n
			if !fn(fieldNum, wireType, nil, v) {
				return
			}
		case 1:
			if pos+8 > len(data) {
				return
			}
			if !fn(fieldNum, wireType, data[pos:pos+8], 0) {
				return
			}
			pos += 8
		case 2:
			length, n := decodeVarint(data[pos:])
			// Compare as uint64: converting a huge length to int wraps negative
			// and used to slip past this bound check.
			if n == 0 || length > uint64(len(data)-pos-n) {
				return
			}
			pos += n
			if !fn(fieldNum, wireType, data[pos:pos+int(length)], 0) {
				return
			}
			pos += int(length)
		case 5:
			if pos+4 > len(data) {
				return
			}
			if !fn(fieldNum, wireType, data[pos:pos+4], 0) {
				return
			}
			pos += 4
		default:
			return
		}
	}
}
