# SiYuan-zhCN-proofread · 思源简中校对

思源笔记简体中文译文（zh-CN.json）的逐条校对工具。单文件桌面程序，双击即用。

## 为什么做这个项目

校对不改原意，只正其文。思源以简体中文为源语言，其余语种均由简体中文翻译，简中译文的准确性同时决定所有下游语种的质量。本项目对 zh-CN.json 做逐条校对，清理繁体直转与机翻残留（如「页签」→「标签页」）。

**本项目不修改思源软件本身**，只针对翻译文件（zh-CN.json）的译文内容；不涉及任何思源程序代码、功能的改动。

**本项目为非官方社区作品**：SiYuan / 思源笔记 是云南链滴科技有限公司的产品名称，本项目与该公司无任何关联、未获其背书；提及该名称仅用于说明校对对象。

项目维护人员自 2008 年起参与 Firefox 汉化（BabelZilla），2011 年起从事 Android 应用中文化，近十年为 Android 音乐播放 APP Poweramp 维护简体中文翻译（作者已授权简中校对人员权限）。

**本项目校对不是标准，肯定会有失误，包括哪里要空格，全角，半角。本项目的每一个词条都欢迎任何人提出异议（欢迎提 issue）——包括对既有校对的推翻。** 正如维护者在其他简中项目中始终接受纠错一样，这里同样如此（如「其它」→「其他」），公文、新闻、出版、软件本地化（微软、Firefox 简中规范）：统一用「其他」，不主动用「其它」。

## 使用（普通用户）

1. 下载 `SiYuan-zhCN-proofread_v1.0.1.zip`（Releases 页），**解压到任意一个独立文件夹**后，运行里面的 `SiYuan-zhCN-proofread.exe`——无需安装，无控制台黑框，无网络端口。校对数据（data.db）自动生成在 exe 旁，整个文件夹即完整程序，迁移备份拷文件夹即可
2. 首次运行空表只有 key 列：先「导入zh-TW.json」（繁体参照，可选），再「导入zh-CN.json」（从思源安装目录 `appearance/langs/` 复制）后显示全部四列。**Release 包内的 data.db 已预置校对成果，导入 zh-CN 文件后会自动合并显示**，无需从零校对
3. 校对：查找过滤（查找按钮前可勾选 key / zh-TW / zh-CN 选择查找范围）/ 双击第四列编辑 / 行尾 ↺ 还原 / 批量替换（弹窗预览 + 一键还原本次替换）；表格按 zh-CN.json 中 key 顺序排列；可选「导入 en.json （可选）」，双击校对时编辑框上方显示英文原文参考；行色：绿=新版新增、淡蓝=简繁字面相同、淡黄=OpenCC 转换一致·无需校对、淡红=需人工校正、橙条=简中已改待重核（图例在导入按钮左侧）
4. 「保存」写入本地数据库；「导出校对结果」弹出另存为窗口

**导出说明（重要，路径与文件名完全由你自由选择）：**

- 默认文件名 `zh-CN.json`（与思源语言文件同名，可直接替换），可改成任何名字、保存到任何位置
- 目标文件已存在时，系统会先弹出「是否替换」确认
- **若要覆盖思源安装目录的语言文件，建议先备份原文件**（复制一份留底，或改名如 `zh-CN.json.bak`），以便随时回退
- 也可以只把导出的校订版发给其他用户直接使用，不必覆盖原文件

## 编译（技术人员）

依赖：Go 1.21+（纯 Go 实现，无 CGO，SQLite 用 modernc.org/sqlite 驱动；简繁判定用 longbridgeapp/opencc，词典编译期内嵌、离线可用）

```bash
go build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o SiYuan-zhCN-proofread.exe .
```

- **必须带 `-tags desktop,production`**：Wails 构建标签，缺失会启动弹 Error 框
- `-H windowsgui`：隐藏控制台黑框
- 图标打包：双击 `build_go.bat`（自动生成图标资源并编译，`app.ico` 需与脚本同目录）
- 验证：`go test ./...` 含语言文件保序往返测试（解析→序列化逐字节一致）

## 数据库结构（可用 DBeaver 查看/更新）

SQLite，库文件 `data.db` 自动建在 exe 旁，可用 [DBeaver](https://dbeaver.io/) 直接打开查看或修改（改库前建议先关闭本程序，避免写冲突）：

**运行期间 data.db 旁会伴生 `data.db-wal` 与 `data.db-shm` 两个文件**，这是 SQLite WAL 模式（写缓存 + 共享内存）的正常工作文件，不是错误产物：

- 程序正常关闭时会自动合并清理；异常退出（断电、强制结束进程）后的残留，下次打开时自动恢复，无需手动删除
- **运行期间请勿删除或移动这两个文件**（会导致数据损坏）
- 想备份校对成果：关闭程序后复制 `data.db` 一个文件即可（此时全部数据已合并进主库）

```sql
entries(id, key, zh_tw, zh_cn, zh_cn_prev, fix_cn, is_new, status)
  -- key：JSON Pointer（RFC 6901），如 /_kernel/2，界面显示为 _kernel_2
  -- zh_tw：繁体参照；zh_cn：简中当前值；zh_cn_prev：简中修改前的原值留底
  -- fix_cn：校对值（与简中相同时也保存实际值）；is_new：新版新增行
  -- status：pending 未核对 | verified 已核对 | stale 简中已修改此条（需重新核对）| obsolete 已废弃（默认隐藏，软删不物理删除）
sync_log(id, ts, source, added, removed, changed)  -- 版本同步历史
meta(k, v)  -- zh_cn_json：zh-CN json 原文与缩进（导出骨架，保证结构与原文件逐字节同构）；en_json：英文参考全文（可选导入）
```

v1.0.0 及更早的 data.db（旧列名 official_cn/official_prev）用新版本打开时自动迁移列名，校对数据无损，无需手动处理；旧库升级后不能再用 v1.0.0 打开。

思源版本更新后，重新「导入zh-CN.json」会自动显示：标记新增词条、已废弃词条、简中已修改的词条、疑似改名的词条，已有校对成果全部保留。

## 致谢

- [DBeaver](https://dbeaver.io/) —— 免费开源的数据库管理工具，本项目的数据维护离不开它
- [SiYuan 思源笔记](https://github.com/siyuan-note/siyuan) —— 优秀的笔记软件

## 项目结构

`main.go` Wails 入口 · `app.go` API 与导出 · `i18n.go` 保序 JSON（Token 流解析，绕开 Go map 字母重排）· `store.go` SQLite · `frontend/dist/` 内嵌前端（Vue3）
