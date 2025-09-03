### 项目进度与任务拆分（GIO）

#### 目标回顾
- 低延迟、低抖动；高吞吐；可预测 GC；可扩展多 poller。
- 初版聚焦 Linux/epoll；非 Linux 提供编译 stub。

#### 里程碑与任务

##### M1：单 poller、LenFlags 头（非批量）、同连接有序（MVP）
- [x] 创建 progress.md 并落地任务拆分
- [ ] 公共 API 骨架：`Cipher`、`Config`、`Handler`、`Conn`、`Server`、`Start/Stop`
- [ ] 非 Linux 平台 stub（保证 macOS 可编译）
- [ ] Listener/Accept + 单 poller 主循环（epoll ET + 非阻塞）
- [ ] LenFlags 2B/4B 非批量帧解析与切帧（Compressed=0 路径）
- [ ] 同连接有序：`OnMessage` 同步执行；`Conn.Go` 置 `asyncBusy`
- [ ] 基础背压阈值与错误回调
- [ ] 基础可观测指标骨架（counter/gauge 占位）

##### M2：多 poller、`SO_REUSEPORT`、高水位背压
- [ ] 多监听/分发到各 poller，连接亲和
- [ ] `writev` 合并策略与 EPOLLOUT 关闭时机完善
- [ ] 高水位发送队列背压与丢弃策略

##### M3：延迟聚合与压缩、时间轮
- [ ] `TxAggregator`：延迟窗口、阈值触发、`Flush()`
- [ ] 批量 pre-image 编码与 zstd 集成
- [ ] 时间轮（1ms）统一调度批量与超时

##### M4：可观测性与自适应
- [ ] 指标完善：P50/P99、批量/压缩比/重试等
- [ ] 批量阈值与窗口自适应

##### M5：安全与零拷贝
- [ ] TLS/kTLS 接入策略
- [ ] 大文件零拷贝（sendfile/splice）

#### 近期执行计划（按顺序）
1) 公共 API 骨架与跨平台 stub，确保 `go build` 通过（macOS）。
2) 单 poller listener/accept/epoll 主循环雏形（Linux）。
3) LenFlags 2B/4B 非批量帧解析与同连接串行处理。

#### 备注
- 默认参数：`NumPollers=runtime.NumCPU()`（M1 可先固定为 1）。
- 代码风格：热路径无指针结构、对象池与环形缓冲、清晰注释。


