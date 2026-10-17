package live

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// ---- t72：直播 FLV 完整性（吸收上游 38799fc / 1.7.4 / 1.7.5）----
//
// 三件事：
//  1. 长度前缀校验：FLV 标签的 3 字节数据长度必须被真实数据填满；末尾半截标签 = 截断包，
//     不能当成正常内容写进产物（上游 38799fc）。
//  2. 媒体帧计数：合并前逐段数音视频标签（type 8/9）。零媒体帧的分段（断流重连后只拿到
//     FLV 头/配置标签）跳过、不否决整场；只有**全部分段**都零帧才失败（上游 1.7.5）。
//  3. 合并判据不再只看产物大小：输入/输出的媒体帧数也要对得上，坏段/非 FLV 单段时保留原
//     分段与旧输出（上游 1.7.4）。

// errNotFLV 表示这不是能识别的 FLV 容器（可能压根不是 FLV，或被破坏到没有头）。
var errNotFLV = errors.New("不是 FLV 容器（缺少 FLV 头）")

// flvScanResult 是一个 FLV 分段的扫描结论。
type flvScanResult struct {
	MediaFrames int   // 音视频标签数（type 8 / 9）
	TotalTags   int   // 全部标签数（含 script / 未知类型）
	Truncated   bool  // 末尾存在「声明长度 > 实际数据」的截断标签
	Complete    int64 // 最后一个完整标签的结束偏移（可直接截断到它）
	TruncatedAt int64 // 截断标签的起始偏移（Truncated 为真时有效）
}

// flvHeaderLen 返回 FLV 头 + PreviousTagSize0 之后的起始偏移。
func flvHeaderLen(data []byte) (int, error) {
	if len(data) < 13 || string(data[:3]) != "FLV" {
		return 0, errNotFLV
	}
	offset := int(binary.BigEndian.Uint32(data[5:9]))
	if offset < 9 || offset+4 > len(data) {
		offset = 9 // 头里的 data offset 不可信时按规范布局（9 字节头 + 4 字节 PrevTagSize0）
	}
	return offset + 4, nil
}

// scanFLV 逐标签扫描 FLV 数据：核对每个标签的 3 字节长度前缀是否被真实数据填满。
//
// 末尾半截标签一律**不计入媒体帧**，并把 Truncated 置真（调用方据此拒绝这个分段或先裁剪）。
// 这是 t72 ① 的核心：截断包不能悄悄进入合并/产物。
func scanFLV(data []byte) (flvScanResult, error) {
	var res flvScanResult
	pos, err := flvHeaderLen(data)
	if err != nil {
		return res, err
	}
	res.Complete = int64(pos)
	for pos+11 <= len(data) {
		tagType := data[pos]
		dataSize := int(uint32(data[pos+1])<<16 | uint32(data[pos+2])<<8 | uint32(data[pos+3]))
		next := pos + 11 + dataSize + 4 // 标签头 + 数据 + PreviousTagSize
		if next > len(data) {
			res.Truncated = true
			res.TruncatedAt = int64(pos)
			return res, nil
		}
		res.TotalTags++
		if tagType == 8 || tagType == 9 {
			res.MediaFrames++
		}
		pos = next
		res.Complete = int64(pos)
	}
	// 尾部还有不足一个标签头的残留字节：同样是截断（不是合法边界）。
	if pos < len(data) {
		res.Truncated = true
		res.TruncatedAt = int64(pos)
	}
	return res, nil
}

// scanFLVFile 与 scanFLV 同判据，但只读标签头、用 Seek 跳过数据：直播分段可能有几百 MB，
// 整读进内存不现实。读失败按「坏段」上抛，由调用方决定保留还是失败。
func scanFLVFile(path string) (flvScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return flvScanResult{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return flvScanResult{}, err
	}
	size := info.Size()

	head := make([]byte, 13)
	if _, err := io.ReadFull(f, head); err != nil {
		return flvScanResult{}, errNotFLV
	}
	start, err := flvHeaderLen(head)
	if err != nil {
		return flvScanResult{}, err
	}

	var res flvScanResult
	res.Complete = int64(start)
	pos := int64(start)
	tagHead := make([]byte, 11)
	for pos+11 <= size {
		if _, err := f.ReadAt(tagHead, pos); err != nil {
			return res, err
		}
		tagType := tagHead[0]
		dataSize := int64(uint32(tagHead[1])<<16 | uint32(tagHead[2])<<8 | uint32(tagHead[3]))
		next := pos + 11 + dataSize + 4
		if next > size {
			res.Truncated = true
			res.TruncatedAt = pos
			return res, nil
		}
		res.TotalTags++
		if tagType == 8 || tagType == 9 {
			res.MediaFrames++
		}
		pos = next
		res.Complete = pos
	}
	if pos < size {
		res.Truncated = true
		res.TruncatedAt = pos
	}
	return res, nil
}

// truncatedSegmentError 把「分段末尾有截断标签」翻成可读错误（写盘路径与合并前共用同一句）。
func truncatedSegmentError(path string, res flvScanResult) error {
	return fmt.Errorf("分段 %s 末尾有截断的 FLV 标签（偏移 %d，已完整解析到 %d 字节）：拒绝把它当完整分段",
		path, res.TruncatedAt, res.Complete)
}

// validateMergeProduct 核对合并产物：必须还是可识别的 FLV，且媒体帧数不少于参与合并的分段合计
// （concat -c copy 应当帧数守恒）。返回 nil 才允许把 staging 改名为正式产物——失败路径因此
// 一定保留原分段与旧产物（上游 1.7.4）。
func validateMergeProduct(stagingPath string, plan liveMergePlan) error {
	if plan.Legacy {
		return nil // 非 FLV 源沿用旧路径：没有帧数可对，交给既有的大小判据
	}
	res, err := scanFLVFile(stagingPath)
	if err != nil {
		return fmt.Errorf("直播分段合成产物不是可识别的 FLV（%v）", err)
	}
	if res.MediaFrames < plan.Frames {
		return fmt.Errorf("直播分段合成产物帧数不足（产物 %d 帧，参与合成的分段合计 %d 帧）",
			res.MediaFrames, plan.Frames)
	}
	return nil
}

// liveMergePlan 是合并前的分段体检结果（t72 ②③）。
type liveMergePlan struct {
	// Kept 是有媒体帧、应当参与合并的分段（保持原顺序）。
	Kept []string
	// Skipped 是零媒体帧的分段（断流重连后只有 FLV 头/配置标签）——跳过，不否决整场。
	Skipped []string
	// Bad 是非 FLV 或读不出的分段：出现任何一段就整场失败（保留原分段与旧输出）。
	Bad []string
	// Frames 是 Kept 的媒体帧总数（供合并后核对输出）。
	Frames int
	// Legacy 表示整场分段都不是可识别的 FLV（例如 HLS/TS 源或合成负载）：沿用旧路径按大小
	// 合并，不做帧数判定——否则会把本来能用的录制判死。
	Legacy bool
}

// planLiveMerge 逐段体检并给出合并计划。
//
// 返回 error 的两种情形都必须让调用方**保留原分段与旧输出**：有坏段、或所有分段都没有媒体帧。
// 只有「部分分段零帧」是可恢复的——那些分段被跳过，其余照常合并（上游 1.7.5）。
func planLiveMerge(segPaths []string) (liveMergePlan, error) {
	var plan liveMergePlan
	recognized := 0
	for _, p := range segPaths {
		res, err := scanFLVFile(p)
		if err != nil {
			plan.Bad = append(plan.Bad, p)
			continue
		}
		recognized++
		if res.MediaFrames == 0 {
			plan.Skipped = append(plan.Skipped, p)
			continue
		}
		plan.Kept = append(plan.Kept, p)
		plan.Frames += res.MediaFrames
	}
	if recognized == 0 {
		// 整场都不是可识别的 FLV：不猜，沿用旧路径（按大小合并；单段直接改名）。
		// 这样非 FLV 的直播源（或既有用例里的合成负载）不会被误判成「坏段」。
		plan.Legacy = true
		plan.Kept = append([]string{}, segPaths...)
		plan.Bad = nil
		return plan, nil
	}
	if len(plan.Bad) > 0 {
		return plan, fmt.Errorf("直播分段里有 %d 个不是可识别的 FLV（%v），整场不合并（原分段与旧产物已保留）",
			len(plan.Bad), plan.Bad)
	}
	if len(plan.Kept) == 0 {
		return plan, fmt.Errorf("直播分段全部没有媒体帧（%d 个），没有可合并的内容（原分段已保留）", len(segPaths))
	}
	return plan, nil
}
