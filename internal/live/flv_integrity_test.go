package live

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- t72：FLV 完整性（长度前缀 / 零帧段 / 坏段）的纯函数用例 ----
//
// 全部离线、不碰 socket、不调 ffmpeg：跨平台判据（windows-latest 上回环流式 harness 不可靠，
// 这里刻意不用它）。

type testTag struct {
	typ  byte
	data []byte
}

// flvBytes 按 FLV 规范拼一段流：9 字节文件头 + PreviousTagSize0 + 若干标签（含各自的 PrevTagSize）。
func flvBytes(tags ...testTag) []byte {
	out := []byte{'F', 'L', 'V', 1, 0x05, 0, 0, 0, 9, 0, 0, 0, 0}
	for _, tg := range tags {
		n := len(tg.data)
		out = append(out, tg.typ, byte(n>>16), byte(n>>8), byte(n), 0, 0, 0, 0, 0, 0, 0)
		out = append(out, tg.data...)
		total := 11 + n
		out = append(out, byte(total>>24), byte(total>>16), byte(total>>8), byte(total))
	}
	return out
}

func writeSeg(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScanFLVCountsMediaFrames 钉住基线：只有 type 8/9 算媒体帧，script（18）不算。
func TestScanFLVCountsMediaFrames(t *testing.T) {
	data := flvBytes(
		testTag{typ: 18, data: []byte("onMetaData")},
		testTag{typ: 9, data: []byte{0x17, 0x00, 0x00}},
		testTag{typ: 8, data: []byte{0xAF, 0x01}},
	)
	res, err := scanFLV(data)
	if err != nil {
		t.Fatalf("scanFLV: %v", err)
	}
	if res.MediaFrames != 2 || res.TotalTags != 3 {
		t.Errorf("媒体帧/标签数 = %d/%d, want 2/3", res.MediaFrames, res.TotalTags)
	}
	if res.Truncated {
		t.Error("完整数据不该判截断")
	}
	if res.Complete != int64(len(data)) {
		t.Errorf("Complete = %d, want %d", res.Complete, len(data))
	}
}

// TestScanFLVRejectsTruncatedTag 是 t72 ① 的核心：截断的长度前缀包必须被识别成截断，
// 且那个半截标签**不计入媒体帧**。
func TestScanFLVRejectsTruncatedTag(t *testing.T) {
	full := flvBytes(
		testTag{typ: 9, data: []byte{0x17, 0x00, 0x00, 0x00}},
		testTag{typ: 9, data: []byte{0x17, 0x00, 0x00, 0x00, 0x00, 0x00}},
	)
	// 把最后一个标签的数据砍掉 3 字节（声明长度仍然是 6）。
	truncated := full[:len(full)-3]
	res, err := scanFLV(truncated)
	if err != nil {
		t.Fatalf("scanFLV: %v", err)
	}
	if !res.Truncated {
		t.Fatal("截断的标签必须被判为截断")
	}
	if res.MediaFrames != 1 {
		t.Errorf("截断标签不该计入媒体帧：MediaFrames = %d, want 1", res.MediaFrames)
	}
	if res.TruncatedAt <= 0 || res.Complete >= int64(len(truncated)) {
		t.Errorf("截断偏移/完整边界不合理：TruncatedAt=%d Complete=%d len=%d", res.TruncatedAt, res.Complete, len(truncated))
	}
	if err := truncatedSegmentError("seg-001.flv", res); err == nil ||
		!strings.Contains(err.Error(), "截断") || !strings.Contains(err.Error(), "seg-001.flv") {
		t.Errorf("截断错误文案不合格：%v", err)
	}

	// 尾部残留几个字节（连标签头都不够）：同样算截断。
	garbageTail := append(append([]byte{}, full...), 0x01, 0x02, 0x03)
	if res, err := scanFLV(garbageTail); err != nil || !res.Truncated {
		t.Errorf("尾部残留应当判截断：res=%+v err=%v", res, err)
	}
}

// TestScanFLVNotFLV 非 FLV 输入必须能被区分出来（调用方据此按「坏段」处理）。
func TestScanFLVNotFLV(t *testing.T) {
	if _, err := scanFLV([]byte("this is not an flv stream")); err == nil {
		t.Error("非 FLV 应当报 errNotFLV")
	}
	if _, err := scanFLV([]byte("FL")); err == nil {
		t.Error("过短的输入应当报错")
	}
}

// TestScanFLVFileMatchesSlice 钉住文件版（Seek 跳数据）与内存版判据一致——直播分段很大，
// 合并前只能读标签头。
func TestScanFLVFileMatchesSlice(t *testing.T) {
	dir := t.TempDir()
	data := flvBytes(
		testTag{typ: 9, data: []byte{0x17, 0x00}},
		testTag{typ: 8, data: []byte{0xAF, 0x01, 0x02, 0x03}},
	)
	path := writeSeg(t, dir, "ok.flv", data)
	want, err := scanFLV(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanFLVFile(path)
	if err != nil {
		t.Fatalf("scanFLVFile: %v", err)
	}
	if got != want {
		t.Errorf("文件版与内存版不一致：got %+v, want %+v", got, want)
	}

	// 截断版也要一致。
	truncPath := writeSeg(t, dir, "trunc.flv", data[:len(data)-2])
	wantTrunc, _ := scanFLV(data[:len(data)-2])
	gotTrunc, err := scanFLVFile(truncPath)
	if err != nil {
		t.Fatalf("scanFLVFile(trunc): %v", err)
	}
	if gotTrunc != wantTrunc || !gotTrunc.Truncated {
		t.Errorf("截断文件版判定不一致：got %+v, want %+v", gotTrunc, wantTrunc)
	}
}

// TestPlanLiveMergeMatrix 是 t72 ②③ 的判定矩阵：
//   - 零媒体帧的分段跳过，其余照常合并（不否决整场）；
//   - 全部分段都零帧 → 失败（调用方保留原分段与旧产物）；
//   - 有非 FLV/坏段 → 失败并把坏段列出来。
func TestPlanLiveMergeMatrix(t *testing.T) {
	dir := t.TempDir()
	withFrames := flvBytes(testTag{typ: 9, data: []byte{0x17, 0x00}}, testTag{typ: 8, data: []byte{0xAF}})
	oneFrame := flvBytes(testTag{typ: 9, data: []byte{0x17, 0x00}})
	zeroFrames := flvBytes(testTag{typ: 18, data: []byte("onMetaData")})

	t.Run("零帧段跳过、其余合并", func(t *testing.T) {
		a := writeSeg(t, dir, "a.flv", withFrames)
		b := writeSeg(t, dir, "b.flv", zeroFrames)
		c := writeSeg(t, dir, "c.flv", oneFrame)
		plan, err := planLiveMerge([]string{a, b, c})
		if err != nil {
			t.Fatalf("部分零帧不该失败：%v", err)
		}
		if len(plan.Kept) != 2 || plan.Kept[0] != a || plan.Kept[1] != c {
			t.Errorf("Kept = %v, want [%s %s]", plan.Kept, a, c)
		}
		if len(plan.Skipped) != 1 || plan.Skipped[0] != b {
			t.Errorf("Skipped = %v, want [%s]", plan.Skipped, b)
		}
		if plan.Frames != 3 {
			t.Errorf("帧合计 = %d, want 3", plan.Frames)
		}
	})

	t.Run("全部零帧才失败", func(t *testing.T) {
		a := writeSeg(t, dir, "z1.flv", zeroFrames)
		b := writeSeg(t, dir, "z2.flv", zeroFrames)
		plan, err := planLiveMerge([]string{a, b})
		if err == nil {
			t.Fatal("全部分段都没有媒体帧时必须失败")
		}
		if !strings.Contains(err.Error(), "没有媒体帧") {
			t.Errorf("错误文案应当说明原因：%v", err)
		}
		if len(plan.Kept) != 0 || len(plan.Skipped) != 2 {
			t.Errorf("计划不合理：%+v", plan)
		}
	})

	t.Run("坏段/非 FLV 整场失败", func(t *testing.T) {
		good := writeSeg(t, dir, "g.flv", withFrames)
		bad := writeSeg(t, dir, "bad.flv", []byte("not an flv at all"))
		plan, err := planLiveMerge([]string{good, bad})
		if err == nil {
			t.Fatal("有坏段时必须失败（保留原分段与旧产物）")
		}
		if len(plan.Bad) != 1 || plan.Bad[0] != bad {
			t.Errorf("Bad = %v, want [%s]", plan.Bad, bad)
		}
		if !strings.Contains(err.Error(), "不是可识别的 FLV") {
			t.Errorf("错误文案应当说明原因：%v", err)
		}
	})

	t.Run("单段有帧 → 可合并", func(t *testing.T) {
		only := writeSeg(t, dir, "only.flv", oneFrame)
		plan, err := planLiveMerge([]string{only})
		if err != nil || len(plan.Kept) != 1 || plan.Frames != 1 {
			t.Errorf("单段有帧应当通过：plan=%+v err=%v", plan, err)
		}
	})
}

// TestValidateMergeProduct 钉住 t72 ② 的产物判据：帧数守恒才算成功；非 FLV / 帧数不足都要
// 拒绝（调用方据此保留原分段与旧产物，绝不覆盖旧输出）。
func TestValidateMergeProduct(t *testing.T) {
	dir := t.TempDir()
	twoFrames := flvBytes(testTag{typ: 9, data: []byte{0x17, 0x00}}, testTag{typ: 8, data: []byte{0xAF}})
	oneFrame := flvBytes(testTag{typ: 9, data: []byte{0x17, 0x00}})
	plan := liveMergePlan{Frames: 2}

	if err := validateMergeProduct(writeSeg(t, dir, "ok.flv", twoFrames), plan); err != nil {
		t.Errorf("帧数守恒的产物应当通过：%v", err)
	}
	if err := validateMergeProduct(writeSeg(t, dir, "short.flv", oneFrame), plan); err == nil {
		t.Error("帧数不足的产物必须报错（不能覆盖旧输出）")
	} else if !strings.Contains(err.Error(), "帧数不足") {
		t.Errorf("错误文案应当说明帧数不足：%v", err)
	}
	if err := validateMergeProduct(writeSeg(t, dir, "bad.flv", []byte("garbage")), plan); err == nil {
		t.Error("非 FLV 产物必须报错")
	}
}
