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

### 传输帧（最小头部，LenFlags 可变 2B/4B）
- 采用可变长度 `LenFlags`，以减少小包头部开销：
  - 短头（2B）格式（BE）：
    - bit15: Compressed
    - bit14: Batched（隐含 Compressed=1）
    - bit13: Ext=0（指示短头）
    - bit12..0: Len13（0..8191，载荷在“线路上”的长度，若压缩则为压缩后长度）
  - 长头（4B）格式（BE）：
    - bit31: Compressed
    - bit30: Batched（隐含 Compressed=1）
    - bit29: Ext=1（指示长头；等价地可理解为“len 使用 29 位”）
    - bit28..16: LenHigh13
    - bit15..0: LenLow16
    - 长度 `len = (LenHigh13<<16) | LenLow16`（0..(1<<29)-1）
- 判别规则：读取首 2 字节，若 `Ext=0` 用短头；若 `Ext=1` 则再读取后 2 字节组成长头。
- 非批量帧额外携带 `Api: uint16 (BE)`；批量帧不携带 Api（api 位于批内每条消息）。
- 头部长度：
  - 非批量：短头=2B+Api2B=4B；长头=4B+Api2B=6B
  - 批量：短头=2B；长头=4B

### 负载编码
- 非批量：负载即单条业务消息数据；当 `Compressed=1` 时为压缩后字节（可用于群发预压缩）。
- 批量（Batched=1，隐含 Compressed=1）：
  - 解压后的“批前镜像（pre-image）”使用 `uvarint` 编码：
    - `numMessages`（uvarint）
    - 重复 `numMessages` 次：[ `api(uint16)`, `len(uvarint)`, `payload(bytes)` ]
  - 注意：批内消息不得再次标记压缩或再组合。

-### 限制与建议
- 单帧长度（29 位）协议上限为 512MiB-1（建议默认配置 16~64MiB）。
- `numMessages` 建议 ≤ 64K；单条消息建议 < 2MiB。
- 压缩算法策略：
  - 即发：不压缩（raw message）；
  - 延迟聚合：Zstd(level 1~3)；
  - 对于“已合并且已压缩”的群发负载，通过发送参数指示 `AlreadyMerged`，忽略 api 参数，直接发送。

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
     - 业务处理可返回“需异步”或调用 `c.Go(task)`，框架将消息与连接状态投递到 poller 绑定的工作队列；
     - 标记该连接 `asyncBusy=true`，后续到达的消息处理策略可配置：
       - `PauseRead`：暂时关闭该连接的 EPOLLIN，避免堆积与内存占用；
       - `CopyToPool`：继续读取并切帧，但将消息负载拷贝到 per-poller 字节池后放入 `rxBacklog`；
     - 异步任务完成后通过 eventfd/队列回投“完成事件”，由同一 poller goroutine 清除 `asyncBusy` 并继续处理后续消息（若为 `PauseRead` 策略则重新打开 EPOLLIN）。
  5) 为避免单条慢消息长时间占用，允许设置“一次最多处理 K 条/最多 T 微秒”，以在连接间公平让出。

- 说明：
  - 小包解压（zstd 小数据块）可在 poller 内完成；
  - 大包 zstd 解压可设阈值迁移到“轻量解压线程”，并作为一种异步分流形式；
  - 框架在 `asyncBusy` 期间严格保证同连接不并发处理，确保有序。

### 并发数据结构与无锁队列细化
- 每个 poller 拥有：
  - `acceptQueue`：监听分发（可选）；
  - `ioEvents`：epoll 返回的事件数组（固定容量，复用）；
  - `offloadDoneQueue`（MPSC）：异步任务完成的回投队列（worker→poller），使用无锁 MPSC 链表或 ring，多生产者单消费者（poller）。
  - `timerWheel`：1ms tick 的分层时间轮（连接索引/指针入槽）。
  - `rxSlab`：大页/连续内存组成的“环形字节缓冲”（per-poller，共享）；
  - `rxDescRing`：SPSC 描述符环（poller 生产，处理器消费），每个描述符 `{connID, off, len, api, flags}` 引用 `rxSlab` 片段；
  - `bytesPool`：按尺寸分级（2^k）的小块内存池，作为 `CopyToPool` 策略的承载。
- 每个连接拥有：
  - `rxRing`（SPSC ring，poller→parser/consumer 同线程可退化为指针环，避免分配）；
  - `rxBacklog`（SPSC ring，`asyncBusy=true` 期间积压的已切帧消息队列）；
  - `txStaging`（延迟发送暂存，数组/环形队列：保存 {api,len,payloadRef}，payloadRef 指向共享字节区或偏移）；
  - `txQueue`（待写 iovec 队列，SPSC，poller 专用写，应用/worker 仅提交指针或通过唤醒事件通知）；
  - `busyFlags`：位标志集合（`asyncBusy`、`txPending`、`rxPaused` 等），使用 `atomic` 更新；
  - `ctxData *D`：用户自定义连接数据（泛型 `D`）。
- 无锁原则：
  - poller 是这些结构的唯一消费者；生产者（如 worker）只能通过 MPSC 回投队列/事件触发，不直接并发访问连接内部状态。
  - 读路径中，帧切分直接在 poller 中进行，`rxRing` 可以仅作为原始字节缓冲的滑动窗口（head/tail 原子更新），解析后直接调用 handler，无需跨线程搬运。
  - 写路径中，应用线程提交发送意图采用“跨线程 MPSC → poller 事件”或“与连接同 poller 的 SPSC 入队”，减少 CAS 与锁竞争。

### Conn.Go 异步状态机
- 状态：`Idle` → `AsyncBusy` → `Draining` → `Idle`
- 进入：`c.Go(fn)` 时：
  - 若 `asyncBusy=false`，置 `asyncBusy=true` 并将 `fn` 提交至 poller 绑定的工作队列；
  - 从此刻起，后续到达的消息仅入 `rxBacklog`，不在 epoll goroutine 内立即处理；
  - 若已是 `asyncBusy=true`，继续积压至 `rxBacklog`。
- 完成：worker 执行 `fn(ctx)` 结束后向 `offloadDoneQueue` 回投 `ConnID` 并唤醒 epoll；
- 恢复：epoll goroutine 取回后置 `asyncBusy=false`，并切入 `Draining`：
  - 按公平策略从 `rxBacklog` 逐条处理（最多 K 条或 T 微秒），如未清空则将连接重新排队，避免长连接独占；
  - 清空后回到 `Idle`，恢复“到达即处理”的同步路径。

### RX 缓冲策略与生命周期
- 目标：在同步处理路径上零拷贝（或单拷贝）并避免 GC；在异步/堆积场景下通过策略保证稳定性。
- per-poller `rxSlab`：
  - 大块连续内存切分为环，采用无指针管理（head/tail 偏移）；
  - poller 将读到的报文直接写入 `rxSlab`，并将 `{off,len}` 作为消息视图传给 `OnMessage`；
  - 生命周期：该视图只在 `OnMessage` 调用期间有效；返回后即视为可回收（head 前进）。
- `CopyToPool` 策略：
  - 当连接 `asyncBusy=true` 或需要积压时，对每条消息将负载拷贝到 `bytesPool`（按 2^k 取最近桶）；
  - `rxBacklog` 保存指向池内存的引用；处理完成后归还至池；
  - 用户如在同步路径上选择异步处理当前消息，需自行拷贝一份（例如使用 `c.Alloc(n)` 从池申请再 `copy`）。
- `PauseRead` 策略：
  - 在 `asyncBusy=true` 期间关闭该连接 EPOLLIN，避免 `rxSlab` 被少量忙连接占满；
  - 适用于消息速率较高而异步处理时延较大的连接。
- 用户约定：`OnMessage` 收到的 `msg []byte` 为只读、短生命周期；若需跨协程/延迟处理，务必拷贝。

## 发送路径：立即发送与延迟聚合压缩
- 目标：
  - 立即发送：最小化排队时延，充分利用 `writev` 合并系统调用。
  - 延迟发送：在一个小窗口内（如 10ms）聚合多条小消息并压缩，提高压缩比与吞吐。

- 设计（每连接维护 `TxAggregator`，轮转时间轮统一调度）：
  - 入队 API：
    - `Write(msg, api, opts...)`：可表达以下语义：
      - `Immediate`（默认 raw，无压缩；若 `Compressed=true` 则视为业务侧预先压缩好的报文）
      - `Delayed(window=10ms, algo=zstd)`（进入聚合队列，窗口到期或阈值触发批量压缩）
      - `AlreadyMerged=true`（表示该 `msg` 已经是“批量且压缩好”的负载，忽略 `api`，直接以 Batched+Compressed 头部发送）
      - `Compressed=true`（单帧已压缩；与 `AlreadyMerged` 互斥）
    - 首次延迟入队时，将连接登记到“poller 的时间轮（1ms 精度）”的到期槽位（不创建 per-connection 定时器对象）。
  - 触发条件：
    - 时间轮扫描到期连接；
    - 聚合字节数/消息数阈值达到（如 ≥ 32KiB 或 ≥ 16 条）；
    - 应用显式 `Flush()`。
  - 刷新流程：
    1) 将 `staging` 中的消息编码为批前镜像（`numMessages` + 重复的 `[api,len,payload]`），
    2) 压缩（zstd），生成单帧（Batched=1，隐含 Compressed=1，无 Api 字段），
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
    - Zstd encoder/decoder 放入 per-poller 池，避免频繁 new/free。
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
- 面向性能与灵活性，本库提供“原地加解密钩子”，由用户的泛型类型提供实现：
  - 要求：加密/解密均为就地(in-place)操作且不改变长度；
  - 发送端：在 `writev/sendmsg` 之前调用 `EncryptInPlace(payload)`；
  - 接收端：在帧切分并（若有）解压后，调用 `DecryptInPlace(payload)` 再交付业务；
  - 用户可选择在应用层自行做 TLS/kTLS；若使用 kTLS，应关闭本层钩子以避免重复。

## 关键系统调用与套接字选项
- `EPOLLET` + 非阻塞 fd；
- `TCP_NODELAY`（默认开启）、`TCP_QUICKACK`（按需），`SO_REUSEPORT`（多监听）、`SO_RCVBUF/SO_SNDBUF`（根据 BDP 调优）。
- 可选 `SO_BUSY_POLL` 与 `TCP_DEFER_ACCEPT`；大报文可用 `sendfile/splice`（零拷贝）。

## 公共 API 草案（示意）
- 泛型 Server 与连接：
  - `type Cipher interface { EncryptInPlace(p []byte); DecryptInPlace(p []byte) }`
  - `type Conn[C Cipher] struct { ID uint64; Data C; /* 内部状态省略 */ }`
  - `func (c *Conn[C]) Context() *C`
  - `func (c *Conn[C]) Write(msg []byte, api uint16, opts ...WriteOption) error`
  - `func (c *Conn[C]) Go(task func(ctx context.Context) error)`
  - `func (c *Conn[C]) Flush() error`
  - `func (c *Conn[C]) Close() error`
  - `type Server[C Cipher] struct { ... }`
- Server：
  - `Start[C Cipher](cfg Config[C], h Handler[C]) error`
  - `Stop(ctx context.Context) error`
- Handler：
  - `OnOpen(c *Conn[C])`
  - `OnMessage(c *Conn[C], api uint16, msg []byte) (async bool)`（同连接有序；返回 true 表示业务选择异步分流，可调用 `c.Go`）
  - `OnClose(c *Conn[C], err error)`
- 配置：
  - `type Config[C Cipher] struct { NumPollers int; RxRingSize int; TxRingSize int; TxBatchWindow time.Duration; TxBatchBytes int; TimerWheelTick time.Duration; MaxPayload int; CompressionDefault string; NewCipher func() C; ... }`

## 参数与默认值建议（可配置）
- `NumPollers = 用户配置（缺省 runtime.NumCPU()）`
- `TxBatchWindow = 10ms`，`TxBatchBytes = 32KiB`，`TxBatchMsgs = 16`
- `Compression: immediate=no compress, delayed=Zstd(level=1)`
- `MaxPayload = 16MiB`，`MaxBatchMsgs = 64K`
- `Rx/Tx ring = 1~8MiB/conn`（按业务报文大小与并发调优）
 - `TimerWheelTick = 1ms`

## 关键权衡点与答案（对应你的关注点）
- **Header 设计**：最小 4/6B 头（`LenFlags[compressed|batched]` + 可选 `Api`）；批内以 `[api,len,payload]` uvarint 方案描述，结合“批一定压缩、批外含 api、批内不再嵌套”规则。
- **epoll 实例数量**：支持多个，数量由用户参数决定；poller 为 goroutine（不绑 OS 线程）。
- **同连接有序处理**：默认在 epoll goroutine 内同步处理；遇重 IO 需求通过 `Conn.Go` 异步分流并标记 `asyncBusy`，完成后回投、排空 `rxBacklog` 后继续，始终保证有序。
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


