'use client'

// MetricHelp — the in-app "什么是口径" explainer, shown in a Modal when the user
// clicks the "?" in the 口径 list header. The prose is the single most important
// onboarding surface for the metric model, so it lives here (not in i18n JSON —
// a long markdown body in JSON is unmaintainable) and is mirrored to
// docs/metric-caliber-guide.md for repo reference. Keep the two in sync when
// editing.

import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Components } from 'react-markdown'

// The canonical explanation. Chinese-primary (the product's working language).
export const METRIC_HELP_MD = `
**一句话**:口径(Metric)= 在某个 OD 上、一条最精简的"度量"定义。它只回答"算什么"(\`sum\` / \`count\` / \`avg\` … 哪一列),**不回答**"怎么筛、按什么拆"——那些在你提问时由系统现场拼装。

## 口径 与 OD 的分工

| 层 | 负责什么 | 静态 / 动态 |
|---|---|---|
| **OD(对象)** | 把数据洗干净、命名好、定义表间关系、基线过滤 | 静态,所有口径共享 |
| **口径(Metric)** | 一个聚合度量 + 基础维度(这条口径天生就按它拆) | 静态,一条口径一个口径 |
| **运行时(提问)** | 这次要加哪些维度、筛哪些值、排序、limit | 动态,每次提问不同 |

口径**描述它自己**,不描述任何一次具体查询长什么样。

## 一条口径长什么样

\`\`\`sql
select "ORDER_TYPE", sum("ORDER_QUANTITY") as "total"
from "EARLY_ORDER"
group by "ORDER_TYPE"
\`\`\`

保存时自动拆成三部分:

- **主 OD** = \`EARLY_ORDER\`(从 FROM 推出)
- **度量** = \`sum("ORDER_QUANTITY")\`(\`as total\` 别名保留为结果列名)
- **基础维度** = \`["ORDER_TYPE"]\`(这条口径永远会带的分组)

## ✅ 该写进口径

- **聚合度量**:\`sum / avg / min / max / count / distinct_count(<列>)\`。一条简单口径**只放一个**。
- **基础维度**:这条口径天生就该按它拆的列(可 0 个 = 纯总量,也可多个)。
- **主 OD**、**触发词**(必填 ≥1)、**度量别名**(可选)。

## ❌ 不该写进口径

| 不要写 | 应该放哪 |
|---|---|
| 运行时过滤(\`GEO = 'US'\`) | 提问时传 |
| 可选 / 临时拆分维度 | 提问时传 |
| \`JOIN\` / 跨 OD 的列 | 引擎按 OD 关系自动连 |
| 基线过滤(所有查询都该有的) | 放进 OD |
| \`count(*)\` | 用 \`count(<列>)\`(JOIN 下会重复计数) |
| 多聚合 / 嵌套 / 窗口函数 | 走高级 SQL 模式 |

> **判断法则**:这个东西是这条口径**永远成立**的,还是**随提问变化**的?永远成立 → 进口径(或更底层进 OD);随提问变化 → 留给运行时。

## 属性三态(提问时)

同一条口径,一个列在提问里有三种状态:

- **不传** → 该列完全不出现
- **传了、值留空** → 仅作维度展示(\`group by\`,不过滤)
- **传了、且有值** → 过滤 + 展示("过滤即展示")

> 正因如此,一条口径就能回答"各订单类型的早单量""各品牌的早单量""Legion 品牌的早单量"——无需为每种问法各建一条口径。

## 触发词

口径靠触发词被召回。触发词**空格 / 下划线 / 大小写无关**——一条 \`earlyorder\` 同时命中 \`early order\` / \`Early_Order\` / \`EARLYORDER\`。每条口径至少配 1 个触发词。

## 口径的另一半:任务可达器

口径只解决"系统**能**说什么"。运行时还有另一半——**在 LLM 发挥之前**,系统做一次确定性判定:已授权口径**能不能覆盖**这个问题的需求(维度 / 筛选)?

- 能 → 进入 smartquery,正常回答。
- 不能 → 立刻停下,告诉用户"缺哪个维度、相近口径有哪几条、怎么补"。**绝不让 LLM 用沾边的口径硬答。**

这就是**任务可达器**(也叫 任务触达器)。它把"模型答错了"翻译成"本体里少了什么"——给你一个**有地址**的待办,而不是含糊失败。

> 口径 = 白名单,任务可达器 = 门卫。两个一起才闭环:你这里加 / 改一条口径,下次同问题自动放行。

完整理念 + 实现见 \`docs/reachability-judge-guide.md\`。
`.trim()

const mdComponents: Components = {
  h1: ({ children }) => <h2 className="mt-5 mb-2 font-display text-base font-bold text-ink">{children}</h2>,
  h2: ({ children }) => <h2 className="mt-5 mb-2 border-b border-border pb-1 font-display text-sm font-bold tracking-tight text-ink">{children}</h2>,
  h3: ({ children }) => <h3 className="mt-4 mb-1.5 font-display text-[13px] font-semibold text-ink">{children}</h3>,
  p: ({ children }) => <p className="my-2 text-[13px] leading-relaxed text-ink-muted">{children}</p>,
  ul: ({ children }) => <ul className="my-2 ml-4 list-disc space-y-1 text-[13px] leading-relaxed text-ink-muted marker:text-ink-ghost">{children}</ul>,
  li: ({ children }) => <li className="pl-0.5">{children}</li>,
  strong: ({ children }) => <strong className="font-semibold text-ink">{children}</strong>,
  blockquote: ({ children }) => (
    <blockquote className="my-3 border-l-2 border-ink bg-canvas-alt px-3 py-2 text-[13px] leading-relaxed text-ink">{children}</blockquote>
  ),
  code: ({ className, children }) => {
    const isBlock = (className || '').includes('language-')
    if (isBlock) {
      return <code className="font-mono text-[12px] text-ink">{children}</code>
    }
    return <code className="border border-border bg-canvas-alt px-1 py-0.5 font-mono text-[12px] text-accent">{children}</code>
  },
  pre: ({ children }) => (
    <pre className="my-2 overflow-x-auto border border-border bg-canvas-alt p-2.5 leading-relaxed">{children}</pre>
  ),
  table: ({ children }) => (
    <div className="my-3 overflow-x-auto">
      <table className="w-full border-collapse border border-border text-[12px]">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-canvas-alt">{children}</thead>,
  th: ({ children }) => <th className="border border-border px-2 py-1 text-left font-semibold text-ink">{children}</th>,
  td: ({ children }) => <td className="border border-border px-2 py-1 align-top text-ink-muted">{children}</td>,
}

export function MetricHelpContent() {
  return (
    <div className="max-h-[70vh] overflow-y-auto pr-1">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>
        {METRIC_HELP_MD}
      </ReactMarkdown>
    </div>
  )
}
