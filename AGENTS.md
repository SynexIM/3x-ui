# AGENTS.md

本仓的 AI 协作指南统一维护在 [`CLAUDE.md`](CLAUDE.md)（栈、请求生命周期、
分层规则、症状→文件索引都在那里及 `docs/architecture.md`）。
工作区总约束见 `../../CLAUDE.md`。

仓库定位：专线节点侧面板（3x-ui fork，`targetProject: ipline`），开源，
发布走自家 Release；remote 约定 origin=SynexIM/3x-ui，upstream=MHSanaei/3x-ui。

## 产品与工程硬约束（全文见工作区 `../../CLAUDE.md`「产品与设计准则」「工程纪律」；2026-09-28）
- 按可独立使用的完整产品来做，运维一眼能看懂；IPLine 只是调用方之一。
- 凭据按协议分开且独立：UUID → VLESS/VMess；Password → Trojan/Shadowsocks；HY2 独立口令；Mixed/HTTP/SOCKS5 独立用户名＋口令。禁止"用户名=邮箱、口令=Password"回退；面板、订阅、落盘配置三处必须一致。
- 限速只留一套模型（标准/突发/额度/持续 + 池 + class），旧 PIR/CIR/CBS 删除；节点侧无业务词、无默认值（0＝无）；限速层附加延迟 p50 ≤ 1ms、p99 ≤ 3ms，无队头阻塞。
- 界面：保留节点组；class 显示可读名字；改页面附 1440 与 390 两张截图，未附不算完成。迁移一次收口，不留两套并存。
