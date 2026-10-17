# 上游 1.7.4 / 1.7.5 工程硬化对照评估

对照对象：上游（BBDownT / AliverAnme 线）1.7.4、1.7.5 两版里的六项「工程硬化」改动。
本文只做**评估**（现状 / 是否同病 / 最小修法 / 工作量 / 优先级），不改产品代码。
代码位置以本次评估时的 HEAD 为准（模块路径 github.com/QC3284/BBDown-Go）。

约定：优先级 P1 = 该修（用户可感或省资源）、P2 = 顺手修、P3 = 记录/暂不动；
工作量按「含用例与变异验证」的人日估算。

## 总表

| # | 硬化项 | 是否同病 | 优先级 | 工作量 | 一句话结论 |
|---|---|---|---|---|---|
| ① | 多线程续传清单 + 分片布局校验 | **否**（我们没有跨进程分片续传） | P3 | 若做续传 2–3 人日 | 分片每次都从头下；补齐续传时必须同时做布局校验，否则会引入上游那类「布局变了却接着拼」的 bug |
| ② | 单分片失败时取消同下载其余在途分片 | **是** | P1 | 0.3 人日 | 现在只记 firstErr、不 cancel：坏分片失败后其余分片仍把整段下完，白耗流量与时间 |
| ③ | 完整跳过时统一回收跨模式残留 | **是** | P1 | 0.5 人日 | 跳过分支直接 return，.tmp / 续传清单 / .vclip 分片 / .aria2 控制文件都留在磁盘上 |
| ④ | 服务器透传文本净化 | **否（已免疫）** | P3 | 0 | 所有日志参数过 SanitizeLogString；服务端可控的路径段过 SanitizePathSegment |
| ⑤ | 显式工具路径不存在时告警并说明回退 | **是** | P2 | 0.2 人日 | findBinaries 对不存在的 --ffmpeg-path/--mp4box-path/--aria2c-path 静默忽略、悄悄回落 PATH |
| ⑥ | DRM 解密唯一临时文件 + 验证后覆盖 | **否（已免疫）** | P3 | 0 | DecryptStream 只有 <input>.dec 一个临时件，成功才删原件改名；失败删 .dec、原媒体分毫不动 |

---

## ① 多线程续传清单 + 分片布局校验

**上游改动**：多线程下载带续传清单，重启时先校验分片布局（分片数/边界/总长）再决定能否接着拼。

**我们现状**：
- 单线程路径有续传清单：internal/download/downloader.go 的 resumeManifest（约 218–300 行）、
  singleDownload（约 471 行起）把 .tmp 当断点，清单里存「稳定身份 + 总长 + ETag/Last-Modified」，
  manifestMatchesProbe 决定能否 Range 续传，不匹配就丢弃旧前缀（这部分是**有**的）。
- 多线程路径**没有**跨进程续传：multiThreadDownload 规划 clipRange（约 760–780 行）后，
  downloadRange（约 830 行起）对每个分片都是**从头**发 Range 请求覆盖 dest.NNN.vclip，
  失败/取消时把全部分片删掉（约 865–880 行）。因此不存在「旧分片 + 新布局」混拼的窗口。

**是否同病**：否——因为我们没有多线程续传，上游那条「布局校验」在这里没有对应风险面。
但反过来看：**续传缺失**本身是一个可选增强（中断后要整段重下）。

**最小修法（若将来补续传）**：
1. 给多线程路径也写一份清单：{身份, 总长, 分片大小, 分片数, 每个分片的 [from,to]}；
2. 启动时先读清单 + 重探（复用 manifestMatchesProbe 的判据），布局不一致（分片大小/边界/总长
   任一不同）就整组丢弃分片，绝不在旧分片上续拼；
3. 用例：布局一致 → 续传；分片大小变了 / 总长变了 / 身份变了 → 全部重下。

**工作量**：2–3 人日（清单格式 + 校验 + 断点续传 + 并发下的边界用例）。
**优先级**：P3（记录；等有「续传」需求时一并做，避免现在做半套）。

## ② 单分片失败时取消同下载其余在途分片

**上游改动**：任一分片失败 → 立刻取消同一文件其余在途分片（Context 取消），不再白下。

**我们现状**（internal/download/downloader.go，multiThreadDownload 约 795–865 行）：
- 有 sem 限流 + sync.WaitGroup + firstErr（errMu 保护）+ notSupported 原子标志；
- 失败时只 recordErr（不 cancel）：notSupported 只挡住**尚未启动**的分片（循环开头 break），
  已经跑起来的分片会继续把整段下完；最后 wg.Wait() 才返回 firstErr；
- 失败后会把全部分片删掉（约 865–880 行），所以不会留下脏分片——只是白耗流量/时间。

**是否同病**：**是**（部分同病：不取消在途，但不会把脏分片留在盘上）。

**最小修法**：
1. multiThreadDownload 里 ctx, cancel := context.WithCancel(ctx)（defer cancel）；
2. recordErr 第一次记录时调用 cancel()（errMu 内判断 firstErr 为 nil 再 cancel）；
3. 注意与既有语义对齐：ErrRangeNotSupported 也要 cancel（它后续会让上层改用单线程路径）；
4. 用例：让 1 号分片的假服务器返回 500、其余分片慢速吐数据 → 断言其余分片在 firstErr 出现后
   很快收到 context.Canceled 且不再写盘（用可观测的 channel/字节数，不用固定 sleep）。

**工作量**：0.3 人日。**优先级**：P1（省流量 + 快速失败，用户可感）。

## ③ 完整跳过时统一回收跨模式残留

**上游改动**：判定「产物已完整、跳过下载」时，顺手清掉各模式遗留的中间文件，避免磁盘垃圾与
「下次又续到旧前缀」的困惑。

**我们现状**：
- 跳过判定在 internal/download/downloader.go 的 DownloadFile：existingDownloadComplete
  （t68 新增，约 340–360 行）——大小相等且**没有** aria2c 控制文件才算完整；
- 命中完整时只打一行「已存在, 跳过下载...」然后 return nil，**不清理**任何残留；
- 我们可能留下的残留类型（各有出处）：
  - 单线程：dest.tmp + dest.tmp.meta（续传清单，真实名 .tmp.meta——勘误：初版误写 .tmp.json，t89 已按真实名实现）；
  - 多线程：dest.NNN.vclip（clipPath，约 911 行）；
  - aria2c：dest.aria2（aria2ControlPath，约 333 行；注意 t68 之后它还算「未完成」证据，
    只有在**非** aria2c 路径成功重写后才清理）；
  - DRM：dest.dec（mp4decrypt 的输出，失败会删、成功会被改名覆盖）；
  - live：<path>.segs/session-*/seg-NNN.flv（internal/live/live.go；我们的 live 合成临时名是
    .staging，**不是**上游那个 .merging）；
  - 混流：muxer 自身不产生临时文件（直接写目标路径；live 合成是 .staging）。
- 逐文件清理点分散在：workflow.go 1094 行「清理临时文件...」、downloader.go 的失败分支
  （约 505–560、604–612、865–880 行）。

**是否同病**：**是**（跳过分支不回收；用户会看到一堆 .tmp/.vclip/.json 残留，
而且 .tmp 里可能是上一次中断的前缀）。

**最小修法**：
1. 在 existingDownloadComplete 返回 true 的分支里调用一个 cleanupResidue(destPath)：
   删 dest.tmp、dest.tmp.meta、dest.NNN.vclip（按目录 glob 精确匹配 basename 前缀）、
   dest.dec；
   **不要**删 dest.aria2——它在那条分支里本来就不该存在（存在就不算完整）。
2. live 的 .segs 残留已有「此前保留的录制会话」告警（live.go staleSessions），不要自动删
   （那是用户唯一的恢复副本）；只在文档/提示里说清。
3. 用例：先造完整产物 + 各种残留 → 调用 DownloadFile（假服务器）→ 断言一次 GET 都没发
   （仍然跳过）+ 残留全清；再造「只有 aria2 控制文件」的残件 → 断言**不**清理它、走重下。

**工作量**：0.5 人日。**优先级**：P1（磁盘垃圾 + 误导性续传前缀）。

## ④ 服务器透传文本净化

**上游改动**：服务端返回的提示文本（play_limit 文案、批量失败明细、稍后再看、评论错误消息、
以及 <ownerMid> 这类文件名占位）在镜像 --host / --insecure 下可能被注入控制字符/换行，
展示与落盘前统一净化。

**我们现状**：
- 日志层：internal/util/logger.go 的 sanitizeLogArgs（约 80–95 行）对**所有** util.Log* 参数
  调 SanitizeLogString（约 41 行起），换行/控制字符被压平 → 单行日志无法被服务器文本撑破；
- 路径层：internal/util/file.go 的 SanitizePathSegment（约 126 行起）用于所有**服务端可控**的
  路径段：字幕语言键（subtitle.go 多处、subtitle_web.go:82）、m3u 标题（m3u.go:252）等；
- 文件名占位符：<ownerMid> 来自接口解析出的 OwnerMid（数字），FormatSavePath 走的是同一套
  路径净化；NFO/m3u 侧车写入前也过净化。

**是否同病**：**否**（已免疫）。证据：logger.go:87/89 覆盖全部日志参数；file.go:126 覆盖所有
服务端可控路径段；两处都有既有用例（internal/util 的 logger/file 套件）。

**残留风险（记录，不修）**：play_limit 的 limit_play_reason 文案、评论错误消息目前原样进日志
（已净化换行），但不做「语义过滤」——这与上游一致（上游也只做字符级净化）。

**工作量**：0。**优先级**：P3。

## ⑤ 显式工具路径不存在时告警并说明回退

**上游改动**：--ffmpeg-path/--mp4box-path/--aria2c-path/--wvd-path/--mp4decrypt-path 指向的
文件不存在时，**明确告警**并说明将回退到 PATH 查找（而不是静默忽略）。

**我们现状**（internal/workflow/workflow.go 的 findBinaries，约 2074–2100 行）：
- 显式路径存在 → 采用；不存在 → **静默忽略**，随后静默回落 PATH（ffmpeg/mp4box/aria2c 三处）；
- DRM 两个路径（--wvd-path/--mp4decrypt-path）走 internal/drm/detect.go 的 ResolveAssets：
  显式路径不存在时按「缺失」处理，前置检查会报错并列出缺什么——但**不会**点名「你给的路径不存在」；
- 用户侧后果：路径打错时 ffmpeg 用了 PATH 里另一个版本，或者 DRM 一直报「缺 mp4decrypt」而
  用户以为自己已经指定了。

**是否同病**：**是**（muxer/aria2c 三处静默；DRM 两处部分说明但不点名路径）。

**最小修法**：
1. findBinaries 三处 else 分支各加一行 util.LogWarn：
   「--ffmpeg-path 指向的 X 不存在，改为从 PATH 查找 ffmpeg（若这是笔误请修正）」；
2. ResolveAssets 保持「缺失」语义不变（前置检查已给出可操作指引），只在错误文案里补上
   「你显式指定的路径 <p> 不存在」这一句（drm/detect.go 的 PrerequisiteError 里加一条 hint）；
3. 用例：假路径 + 假 PATH → 断言 Warn 文案含选项名与原路径；DRM 侧断言 hint 含给出的路径。

**工作量**：0.2 人日。**优先级**：P2（排障成本高、改动极小）。

## ⑥ DRM 解密唯一临时文件 + 验证后覆盖

**上游改动**：解密只用**一个**临时文件，产物验证通过后才覆盖原媒体；失败保留原媒体文件。

**我们现状**（internal/drm/mp4decrypt.go，t45 重写）：
- 临时文件唯一：output = input + ".dec"（38 行）；
- 只在成功路径上：确认输出存在且非空（约 82–90 行）→ os.Remove(input)（75 行）→
  os.Rename(output, input)（79 行）；
- 失败路径：os.Remove(output) 后返回错误，**输入文件从未被删除/截断**；
- 超时/取消：同样只删 .dec，原媒体保留。

**是否同病**：**否**（已免疫）。证据：internal/drm/mp4decrypt_test.go 的
TestDecryptStreamFailureCleansUp（失败后输入内容不变、不留 .dec）、
TestDecryptStreamUsesKeyArgument（成功路径产物替换 + 不留 .dec）。

**工作量**：0。**优先级**：P3。

---

## 候选清单（按优先级）

| 优先级 | 项 | 工作量 | 触发条件 |
|---|---|---|---|
| P1 | ② 分片失败取消在途分片 | 0.3 人日 | 随时可做（纯收益） |
| P1 | ③ 跳过时回收跨模式残留 | 0.5 人日 | 随时可做（用户可感） |
| P2 | ⑤ 显式工具路径告警 | 0.2 人日 | 随时可做 |
| P3 | ① 多线程续传（含布局校验） | 2–3 人日 | 有「中断后续传」需求时 |
| P3 | ④ 透传文本净化 | 0 | 已免疫，仅记录 |
| P3 | ⑥ DRM 临时文件语义 | 0 | 已免疫，仅记录 |

合计 P1+P2 ≈ 1 人日；P3 的 ① 若要做是独立功能批次。

## 评估方法说明

- 「同病」判定标准：我们存在与上游改动**同一个用户后果**（白耗资源/磁盘垃圾/误导性报错），
  而不是「代码长得像」；
- 「已免疫」必须给代码位置 + 既有用例（本文 ④⑥ 均给了 file:line 与用例名）；
- 本文不涉及 upstream 的其它硬化项，也不改动任何产品代码。
