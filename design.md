## GIO 基于 epoll 的 Reactor 网络库设计方案

### 目标
- **低延迟、低抖动**：在高并发长连接场景下保持 P99 延迟稳定。
- **高吞吐**：单机百万连接、10GbE 带宽下仍能稳定工作。
- **可预测 GC 行为**：通过无指针数据结构、环形缓冲与对象复用降低 GC 压力。
- **可扩展**：多 epoll/poller 并行，线性扩展到 CPU 数量及网卡队列数。

### 非目标
- 不以兼容 `net` 标准库 API 为首要目标。
- 初版仅面向 Linux（epoll）；其他平台可后续扩展为 kqueue/IOCP。

## 总体架构（Reactor 多路复用）
- **多 Reactor（poller）+ per-connection 亲和**：
  - `numPollers` 由用户配置（默认 `runtime.NumCPU()`）；每个 poller 持有一个 epoll fd，运行在独立 goroutine（不使用 `runtime.LockOSThread`）。
  - 采用 `SO_REUSEPORT` 多监听模型，将新连接直接分散到各 poller，减少跨线程迁移。
  - 每个连接“固定”在创建它的 poller 上：读写事件、状态变更均由该 poller 驱动。

- **三层流水线**：
  - L1：poller 事件层（epoll，ET 边缘触发，非阻塞，批量 read/writev）。
  - L2：协议层解析与封包（按帧解析、可选解压/切分批量消息）。
  - L3：业务处理层（每连接顺序保证的串行执行器，跨连接并行）。

- **唤醒通道**：跨线程从业务/worker 推送写入意图或定时器到 poller，使用 `eventfd` 注册到相应 epoll。

## 协议与消息头（Header）设计

### 设计目标（极简头部）
- 仅保留：压缩标志、组合标志、`api(uint16)` 与消息长度；不再引入其他任何可选标志/字段。
- 利用隐含关系优化编码：
  - Batched 一定 Compressed；
  - Batched 外层无 api（api 位于批内每条消息）；
  - 批内消息不再二次压缩或再组合。

### 传输帧（最小头部）
- `LenFlags: uint32 (BE)`：高 2 位为标志，低 30 位为长度（载荷在“线路上”的长度，若压缩则为压缩后长度）。
  - bit31 (1<<31): Compressed
  - bit30 (1<<30): Batched（隐含 Compressed=1）
  - len = `LenFlags & 0x3FFFFFFF`
- 非批量帧额外携带 `Api: uint16 (BE)`；批量帧不携带 Api。
- 头部长度：
  - 非批量：6 字节（LenFlags 4B + Api 2B）
  - 批量：4 字节（仅 LenFlags）

### 负载编码
- 非批量：负载即单条业务消息数据；当 `Compressed=1` 时为压缩后字节（可用于群发预压缩）。
- 批量（Batched=1，隐含 Compressed=1）：
  - 解压后的“批前镜像（pre-image）”使用 `uvarint` 编码：
    - `numMessages`（uvarint）
    - 重复 `numMessages` 次：[ `api(uint16)`, `len(uvarint)`, `payload(bytes)` ]
  - 注意：批内消息不得再次标记压缩或再组合。

### 限制与建议
- 单帧长度（低 30 位）上限建议 ≤ 1GiB（默认配置 16~64MiB）。
- `numMessages` 建议 ≤ 64K；单条消息建议 < 2MiB。
- 压缩算法策略：
  - 即发：LZ4 优先；
  - 延迟聚合：Zstd(level 1~3) 优先；
  - 对于“已压缩群发”通过发送参数指示 `PreCompressed`，避免重复压缩。

## 是否需要多个 epoll 实例？
- 结论：在高并发/高带宽场景下，**支持多个 epoll/poller 并由用户配置数量**。
  - 单 epoll 在协议极简且 CPU 充裕时可支撑十万级连接，但容易在读写与业务调度上形成单线程瓶颈，放大抖动。
  - 多 poller 带来更好的 CPU cache 亲和性、减少跨核迁移，降低尾延迟。
- 参数：`NumPollers` 由用户传入（默认 `runtime.NumCPU()`）；poller 以 goroutine 运行，不绑定 OS 线程（不使用 `LockOSThread`）。
- 建议：`NumPollers ≈ min(numCPU, NIC_RX_Queues)`；小规模场景可设为 1 以减少 goroutine 数。

## 接收路径与“同连接有序处理”
- 目标：同一连接内消息严格按到达顺序交付业务；不同连接之间最大化并行度。

- 设计：
  1) poller 在 EPOLLIN 触发后，以 ET 模式循环 `read`/`readv` 直到 `EAGAIN`。
  2) 数据进入该连接的读环形缓冲；在 poller goroutine 内完成帧切分与必要的解压：
     - 非批量：直接得到一条消息（Api, Payload）。
     - 批量：解压后按 pre-image 逐条还原（Api, Payload）。
  3) 默认在 epoll goroutine 内“同步调用业务处理”（零调度 hop），天然保证同连接串行。
  4) 异步分流（需要额外 IO 如 RPC/DB）：
     - 业务处理可返回“需异步”或调用 `Offload(c, task)`，框架将消息与连接状态投递到 poller 绑定的工作队列；
     - 标记该连接 `asyncBusy=true`，epoll goroutine 暂停对该连接后续消息处理（但仍可读入缓冲并做帧切分，避免读阻塞）；
     - 异步任务完成后通过 eventfd/队列回投“完成事件”，由同一 poller goroutine 清除 `asyncBusy` 并继续处理后续消息。
  5) 为避免单条慢消息长时间占用，允许设置“一次最多处理 K 条/最多 T 微秒”，以在连接间公平让出。

- 说明：
  - 小包与 LZ4 解压建议在 poller 内完成；
  - 大包或 Zstd 解压可设阈值迁移到“轻量解压线程”，并作为一种异步分流形式；
  - 框架在 `asyncBusy` 期间严格保证同连接不并发处理，确保有序。

## 发送路径：立即发送与延迟聚合压缩
- 目标：
  - 立即发送：最小化排队时延，充分利用 `writev` 合并系统调用。
  - 延迟发送：在一个小窗口内（如 10ms）聚合多条小消息并压缩，提高压缩比与吞吐。

- 设计（每连接维护 `TxAggregator`，轮转时间轮统一调度）：
  - 入队 API：
    - `Write(msg, api, Immediate)`：直接组帧（LenFlags + Api + Payload）。若 `opts.PreCompressed=true`，则置位 Compressed 且跳过压缩；尝试 `writev`，若部分写则挂入发送链表并打开 EPOLLOUT。
    - `Write(msg, api, Delayed, window=10ms, algo=zstd)`：消息暂存至 `TxAggregator.staging`；首次进入在“poller 的时间轮（1ms 精度）”登记该连接的到期槽位（不创建 per-connection 定时器对象）。
  - 触发条件：
    - 时间轮扫描到期连接；
    - 聚合字节数/消息数阈值达到（如 ≥ 32KiB 或 ≥ 16 条）；
    - 应用显式 `Flush()`。
  - 刷新流程：
    1) 将 `staging` 中的消息编码为批前镜像（`numMessages` + 重复的 `[api,len,payload]`），
    2) 压缩（zstd/lz4），生成单帧（Batched=1，隐含 Compressed=1，无 Api 字段），
    3) 使用 `writev`/`sendmsg` 写出；若 EAGAIN，缓存剩余 iovec，注册 EPOLLOUT。
  - 群发支持：
    - 允许“已压缩单帧”在业务侧预构建为共享只读缓冲（`SharedPayload`，原子引用计数），多连接复用；
    - 每个连接仅拼装 4~6B 头部并 `writev(head, shared)`，避免重复压缩与拷贝。
  - Nagle/延迟 ACK：默认启用 `TCP_NODELAY`，避免与应用层延迟窗口叠加引入不可控时延；可按连接配置。

- 写出实现细节：
  - 优先使用 `writev` 将头与载荷（或多个帧）一次性写出。
  - 仅在实际发生未写完时才打开 EPOLLOUT；写空后立即关闭 EPOLLOUT（水平触发风暴规避）。
  - 发送高水位与背压：当连接发送队列累计字节超过阈值，暂停上游继续入队（或快速失败/丢弃低优先级）。

## 内存管理与 GC 规避
- 原则：热点路径只操作“无指针”的缓冲与结构，最大化复用，避免短生命周期小对象分配。

- 数据结构与策略：
  - 读/写环形缓冲（per-connection）：
    - 固定容量、2 的幂次对齐，索引取模；仅存放 `[]byte` 的原始区段（或偏移+长度），不存放指针指向 Go 对象。
    - 大容量缓冲可用 `mmap`/`syscall.Mmap` 分配（可选），减少 Go 堆扫描压力；注意与 `unsafe.Slice` 的封装安全性。
  - 对象池：
    - `sync.Pool` 用于短期临时对象（帧描述符、压缩器上下文、TLV 缓冲等）。
    - 长期复用结构（连接状态、环形缓冲）在连接生命周期内持有，连接关闭时整体回收。
  - 压缩器复用：
    - LZ4/Zstd encoder/decoder 放入 per-poller 池，避免频繁 new/free。
  - 无锁 SPSC：
    - poller→parser、parser→ConnActor、app→TxAggregator 皆使用 SPSC 环，减少锁与 GC。
  - 减少写屏障：
    - 热路径结构尽量不含指针或使用 `uintptr`/索引引用大页内存。
  - GC 调优：
    - 建议设置 `GOGC` 较高（如 200~400），并监控堆增长；
    - 使用直方图监测分配热点，持续下沉到池或环结构。

对比：
- `sync.Pool` 适合“突发后很快可回收”的小对象；
- 环形缓冲适合“持续高频复用”的大块字节流；
- 本设计两者结合：关键数据通路优先环形缓冲，周边临时元数据用 `sync.Pool`。

## 背压、流控与丢弃策略
- 接收侧：当 `ConnActor` 或上游 worker 队列超阈值，poller 暂停对该连接的 EPOLLIN 处理（或仅读丢弃至帧边界），避免内存膨胀。
- 发送侧：当发送队列超阈值，拒绝新消息或丢弃低优先级；提供按优先级丢弃与错误回调。
- 全局：按 poller 维度与进程维度双阈值，避免极端流量导致整体雪崩。

## 定时器
- 每个 poller 内置分层时间轮（精度 1ms）：O(1) 近似复杂度，批量处理“延迟发送窗口”“心跳/超时”“慢连接降级”。
- 连接 idle/lease 超时由时间轮驱动，过期触发关闭或探测。
 - 不创建 per-connection 定时器对象；连接在时间轮槽位中以“指针/索引”登记与复用。

## 可观测性
- 指标：
  - 每 poller：处理事件数、系统调用次数、P50/P99 延迟、丢包/丢帧、发送/接收字节。
  - 每连接：队列长度、重试写次数、批量大小分布、压缩比、拥塞状态。
- 日志：
  - 采样打印关键状态变更；
  - 可选 trace（按连接或按 StreamId）。

## 安全与加密
- 初版建议提供“用户态 TLS”与“kTLS（Linux 5.6+）”两种可选路径：
  - 用户态 TLS：复用成熟库（如 crypto/tls），将 TLS record 作为应用帧载荷；
  - kTLS：通过 `setsockopt` 启用内核 offload，减少用户态拷贝与 CPU。
- Header 在加密通道外层（元信息明文）；批内消息在 TLS 保护下仍按本协议解包。

## 关键系统调用与套接字选项
- `EPOLLET` + 非阻塞 fd；
- `TCP_NODELAY`（默认开启）、`TCP_QUICKACK`（按需），`SO_REUSEPORT`（多监听）、`SO_RCVBUF/SO_SNDBUF`（根据 BDP 调优）。
- 可选 `SO_BUSY_POLL` 与 `TCP_DEFER_ACCEPT`；大报文可用 `sendfile/splice`（零拷贝）。

## 公共 API 草案（示意）
- Server：
  - `Start(cfg Config, h Handler) error`
  - `Stop(ctx context.Context) error`
- Handler：
  - `OnOpen(c Conn)`
  - `OnMessage(c Conn, api uint16, msg []byte) (async bool)`（同连接有序；返回 true 表示业务选择异步分流）
  - `OnClose(c Conn, err error)`
- Conn：
  - `Write(msg []byte, api uint16, opts ...WriteOption) error`（立即或延迟、压缩策略、截止时间、PreCompressed）
  - `WriteCompressed(payload []byte, api uint16) error`（业务侧已压缩单帧发送）
  - `Offload(task func(ctx context.Context) error)`（在消息处理路径中调用，将当前消息转交异步队列，完成后自动回投继续该连接处理）
  - `Flush() error`
  - `Close() error`
- 配置：
  - `NumPollers`（用户传入）、`RxRingSize`、`TxRingSize`、`TxBatchWindow`、`TxBatchBytes`、`TimerWheelTick=1ms`、`MaxPayload`、`CompressionDefault`、高水位阈值等。

## 参数与默认值建议（可配置）
- `NumPollers = 用户配置（缺省 runtime.NumCPU()）`
- `TxBatchWindow = 10ms`，`TxBatchBytes = 32KiB`，`TxBatchMsgs = 16`
- `Compression: immediate=LZ4, delayed=Zstd(level=1)`
- `MaxPayload = 16MiB`，`MaxBatchMsgs = 64K`
- `Rx/Tx ring = 1~8MiB/conn`（按业务报文大小与并发调优）
 - `TimerWheelTick = 1ms`

## 关键权衡点与答案（对应你的关注点）
- **Header 设计**：最小 4/6B 头（`LenFlags[compressed|batched]` + 可选 `Api`）；批内以 `[api,len,payload]` uvarint 方案描述，结合“批一定压缩、批外含 api、批内不再嵌套”规则。
- **epoll 实例数量**：支持多个，数量由用户参数决定；poller 为 goroutine（不绑 OS 线程）。
- **同连接有序处理**：默认在 epoll goroutine 内同步处理；遇重 IO 需求通过 `Offload` 异步分流并标记 `asyncBusy`，完成后回投继续，始终保证有序。
- **发送模型（立即/延迟/压缩）**：`TxAggregator` + 1ms 时间轮，无 per-connection 定时器；支持 `PreCompressed` 与群发共享负载；`writev` 合并写；必要时开启 EPOLLOUT。
- **GC 规避**：读写环形缓冲为主、对象池为辅；热路径无指针；压缩器复用；时间轮槽位复用，避免细粒度定时器对象。

## 风险与缓解
- 大包解压阻塞 poller：设置“大包阈值”将解压委派至专用线程，完成后再回投。
- 批量窗口导致尾延迟：对“带截止时间”的消息跳过延迟；或采用“动态窗口”（基于队列压力收缩窗口）。
- 过度聚合造成 head-of-line 阻塞：限制批大小/时间，启用优先级消息旁路（Immediate）。
- 内存占用：严格高水位与背压策略；环形缓冲按需扩容但有上限；监控并报警。

## 里程碑
- M1：单 poller、无压缩、固定 16B 头、同连接有序（ConnActor）
- M2：多 poller + SO_REUSEPORT、`writev`、高水位背压
- M3：延迟聚合 + LZ4/Zstd、时间轮
- M4：可观测性、批量阈值自适应调优
- M5：TLS/kTLS、零拷贝大文件传输

## 参考实现与灵感
- Netty（多 Reactor、pipeline）、libuv/libevent、Seastar（per-core 模型）、Nginx（事件驱动）、Cloudflare ztunnel（批量与零拷贝实践）。


