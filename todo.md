# 项目 TODO 计划

## 范围与目标
- 支持 Linux epoll 与 macOS kqueue。
- 提供服务端 SDK 与客户端 SDK。
- 提供端到端示例（服务端进程 + 客户端进程）完成消息传输。
- 严格实现 design.md 的协议与行为特性。

## 里程碑
- M1：协议编解码、环形缓冲、Linux epoll 单 poller 路径、最小 Server/Conn、echo 示例与 e2e 测试
- M2：多 poller + SO_REUSEPORT、writev/EPOLLOUT、高水位背压、时间轮与 TxAggregator
- M3：kqueue（macOS）、Zstd 池化、异步分流、可观测性占位

## 任务分解
1. 协议编解码（LenFlags 2B/4B + Api + 批前镜像 + uvarint）
2. Zstd 编解码器池化
3. 基础环形缓冲与简化内存池
4. 抽象 Poller 接口并实现 Linux epoll
5. 实现 macOS kqueue Poller
6. 实现 Server：多 REUSEPORT 监听/连接归属/生命周期管理
7. 实现 Conn：读写路径、writev、EPOLLOUT、高水位背压
8. 实现 1ms 时间轮与 TxAggregator（延迟聚合+阈值触发+Flush）
9. 实现 Conn.Go 异步分流与 rxBacklog 排空
10. 实现 Client SDK（Dial/Write/回调处理）
11. 创建 echo 示例（server/client 两个进程）
12. 端到端测试：消息往返验证与延迟窗口测试
13. 基础指标与日志采样（可观测性占位）

## 执行顺序（建议）
- 先完成 1、3、4、6、7、11、12（M1），确保最小可用路径可运行。
- 然后推进 2、8、9（M2），完善性能路径。
- 最后补齐 5、10、13（M3）。
