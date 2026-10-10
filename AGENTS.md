# AGENTS.md

`rweg`-`read`,`write`,`edit`,`grep` commands replacement for bash environment

_These tools are still under developement and experimental. They may not auto-sourced in your bash command, but you can try use the latest dist binaries as alternatives of your read/write/edit/grep tools._

## 基础信息

- lang：go
- 用途：bash里的读写查工具的替代品（实验性质，不保证一定能降token用量）

## 硬规则

- 临时文件落`.temp/`
- 所有文档和代码注释必须描述当前状态，不得记录任何 AI 历史修改记录，发现违反立刻更改。更新当前状态时，不得将未确认的实现偏差写成新的业务规则。
  --比如某次开发工作临时规划了Phase1到Phase5阶段，那么任何文档不允许记录phase1-phase5相关字段描述，只需要按照代码和项目现状更新文档状态即可
	--比如某次某次开发需求A决策时当前项目现状，B决策时这次开发任务的决策，且A决策作废，那么文档记录就不能留下A决策的历史，只需要记录这次新的决策B。
- 测试和编码分离——没叫你测你就别动手测，让你只管测试就别动代码。