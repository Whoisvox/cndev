# ── ① 开工前：先拉最新，别在过期代码上写
git switch dev
git pull --rebase          # 有同事推过就rebase，历史是一条线，别多出merge气泡

# ── ② 改代码（随便改，别急着add）

# ── ③ 改完先“分篮”：看清楚我动了哪几件事
git status
# 比如：
#   modified: src/cdn.py        ← 新功能
#   modified: README.md         ← 顺手补个字
#   modified: .github/ci.yml    ← 流水线调整

# ── ④ 按“事”分组 add（不用 add . !）
git add src/cdn.py
git commit -m "feat: 按地域选CDN节点"

git add .github/ci.yml
git commit -m "ci: dev 推送自动跑单测"

git add README.md
git commit -m "docs: 补地域开关说明"

# ── ⑤ 确认没漏“脏东西”（调试print、node_modules、.env）
git status     # 应该干净；若有不想提的 → git restore --staged xxx

# ── ⑥ 推（第一次新分支带 -u，之后裸 push）
git push -u origin dev     # 仅首次
# 之后：
git push