// Pure state helpers for the chat composer, kept apart from the component so
// the streaming-target races that garbled transcripts can be tested head-on
// (see chatstate.test.ts).

/** Apply fn to the bubble the stream is actually writing to: the newest
 *  message still marked streaming — never blindly the last message. A
 *  transcript refetch racing the stream can leave a stale message at the
 *  tail, and a patch aimed there writes the reply into the user's own
 *  bubble. With nothing streaming (the closing patch after an error), fall
 *  back to the last message as before. */
export function patchStreaming<T extends { streaming?: boolean }>(
  msgs: T[],
  fn: (m: T) => T,
): T[] {
  if (msgs.length === 0) return msgs
  let idx = -1
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i]?.streaming) {
      idx = i
      break
    }
  }
  if (idx === -1) idx = msgs.length - 1
  return msgs.map((m, i) => (i === idx ? fn(m) : m))
}

/** The composer's `/` completion query: the input opens the menu only when
 *  its first token starts with a slash and is still being typed. Returns the
 *  first token without the slash (possibly ""), or null when completion is
 *  not active. */
export function slashQuery(input: string): string | null {
  if (!input.startsWith('/')) return null
  const token = input.split(/\s/, 1)[0]?.slice(1) ?? ''
  return token
}
