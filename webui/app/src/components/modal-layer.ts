// SPDX-License-Identifier: AGPL-3.0-or-later

/** 顶层（top layer）追踪：当前打开的模态对话框栈。
 *
 * `showModal()` 做两件事，只有一件是"置顶"：把 <dialog> 放进浏览器的 top
 * layer，**并且**让文档其余部分变成 inert（不可点击、不可聚焦）。popover 能
 * 把任意元素也送进 top layer 参与绘制，但 inert 是跟着 DOM 树继承的，不是跟着
 * 图层——所以一个挂在 app shell 上的 popover，在设置面板打开时会被画在面板
 * 之上，却收不到任何点击。
 *
 * 这就是 2026-11-19 那个「右下角小窗关不掉、只能刷新」的成因：设置在
 * `models.tsx` 里的某个失败通过 toastError 弹出错误 toast，`raise()` 把它推上
 * top layer 画在面板上，而它的 ✕ 落在 inert 区里，点击穿透到面板的
 * backdrop；刷新后组件重新挂载，items 归零，于是"消失"。
 *
 * 结论：任何必须在模态对话框之上**可用**（不只是可见）的浮层，DOM 上必须待
 * 在那个对话框的子树里。本模块维护"当前打开的模态栈"并按打开顺序取栈顶，让
 * 这样的浮层能把自己搬进栈顶对话框、并在最后一个对话框关闭时搬回来。
 */

type Listener = (top: HTMLElement | null) => void

/** 订阅者。多个浮层可以共用同一份栈。 */
const listeners = new Set<Listener>()

/** 每个对话框第一次被看到时分配的序号，用来还原 top layer 的叠放顺序。
 *  DOM 顺序不可靠：`confirm` 可以盖在设置面板上，而它在 DOM 里排在前面。 */
const order = new Map<HTMLElement, number>()
let seq = 0
let current: HTMLElement | null = null
let observer: MutationObserver | null = null

function modalDialogs(): HTMLElement[] {
  const out: HTMLElement[] = []
  if (typeof document === 'undefined') return out
  for (const el of document.querySelectorAll('dialog[open]')) {
    try {
      // 非 `showModal()` 打开的对话框用 `show()`，不进 top layer 也不产生
      // inert，它不需要接管浮层。
      if (el.matches(':modal')) out.push(el as HTMLElement)
    } catch {
      /* 不支持 :modal 的引擎：当作没有模态层，保持固定定位即可 */
    }
  }
  return out
}

function sync(): void {
  const live = modalDialogs()
  const seen = new Set(live)
  for (const el of live) if (!order.has(el)) order.set(el, ++seq)
  for (const el of [...order.keys()]) if (!seen.has(el)) order.delete(el)

  // 最后打开的那个才算栈顶：盖在别人之上的对话框会让下面那个也一起 inert，
  // 把浮层搬进下层等于没搬。
  let top: HTMLElement | null = null
  for (const el of live) {
    if (!top || (order.get(el) ?? 0) > (order.get(top) ?? 0)) top = el
  }
  if (top === current) return
  current = top
  for (const fn of [...listeners]) fn(top)
}

/** 只认 `open` 属性的变化：`showModal()` / `close()` 都会改写它，而其它属性
 *  的抖动（这类页面每秒都有）与我们无关。 */
function start(): void {
  if (observer || typeof MutationObserver === 'undefined' || typeof document === 'undefined') return
  observer = new MutationObserver(sync)
  observer.observe(document.body, { attributes: true, attributeFilter: ['open'], subtree: true })
}

/** 订阅"最上层模态对话框"的变化，立即回调一次当前值；返回退订函数。
 *
 *  回调参数是栈顶对话框元素，没有模态对话框时为 `null`（浮层应当回到自己的
 *  常驻父节点）。 */
export function onModalLayerChange(fn: Listener): () => void {
  start()
  listeners.add(fn)
  sync()
  fn(current)
  return () => {
    listeners.delete(fn)
  }
}
