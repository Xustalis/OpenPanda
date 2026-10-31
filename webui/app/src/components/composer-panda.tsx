// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../i18n'
import { PANDA_REST_POSE_MS, isPandaAwake, restingPose } from './panda-rest'

/* The pet that stands on the composer's top-left corner.
 *
 * It used to be a row of its own above the box — a 56px sprite plus a dot and
 * the word "待命" — which cost the new-chat page a line of empty canvas and
 * read as a status readout for a thread that does not exist yet. The pose is
 * the readout now: it stands when the console is free and cheers while a turn
 * streams. The words stay for screen readers, so the live region still says
 * what the sprite shows.
 *
 * It also keeps office hours. From 08:00 to 20:00 the pose answers the
 * console; the other twelve hours it lies on the edge and turns over every
 * half minute, because a pet that is standing at three in the morning is a
 * pet that has not noticed it is three in the morning. Night wins over the
 * working frame on purpose: the schedule is the user's, and an agent running
 * overnight is exactly the case where nobody needs the sprite shouting about
 * it. Screen readers still hear "Working" — the pose is decoration, the live
 * region is the status.
 *
 * Pressing it is a toy: a press squashes it flat and lets it ring back up, the
 * way a jelly toy answers a poke. Every press is that same press — presses are
 * not counted, there is no streak to chase and nothing to remember. The whole
 * interaction leaves the page alone but for the sprite.
 *
 * The pet also answers for the roster (see composer-pet.tsx), and the
 * gestures share one button:
 *
 *   - a single press opens (or closes) the team card;
 *   - a quick second press — inside 240ms — is a poke: the pending open is
 *     cancelled and the squish plays instead, so a poke never flashes a card
 *     on its way past;
 *   - a third press in the same rhythm gives up on the window entirely: from
 *     then on every press squashes immediately, because somebody tapping
 *     three times in half a second is playing, not navigating. Poking mode
 *     drops 1.2s after the last press, and the next single click opens the
 *     card again.
 *
 * The first press therefore waits out the double-press window before it acts.
 * 240ms is the whole trade: shorter and a deliberate double tap lands after
 * the card already opened, longer and the card feels like it ignored the
 * click. Without an `onPress` the pet has no second job and pokes at once.
 *
 * A running turn used to freeze the whole pet — the press returned early and
 * the roster was unreachable until the answer landed. That was wrong about
 * the card: who works on a thread is a decision people revise *while* the
 * answer streams ("add the reviewer, this is getting big"), and a control
 * that goes dead for the length of a turn reads as broken, not as busy. So a
 * turn now only freezes the toy. During a turn a press opens the card at
 * once, with no double-press window: the working frame owns the sprite, a
 * squish would replace it mid-cheer, and there is no poke to wait for. */
export function ComposerPanda({
  working,
  onPress,
  label,
  expanded,
}: {
  working: boolean
  /** Fired on a single press (after the double-press window). */
  onPress?(): void
  /** Accessible name. Defaults to "poke"; the roster passes a name that
   *  covers both gestures. */
  label?: string
  /** Whether the card this press opens is currently open. */
  expanded?: boolean
}) {
  /* A counter, not a flag: the animation has to replay on every press, and
     bumping the sprite's `key` remounts it, which is the only reliable way to
     restart a CSS animation that is already on the element (re-adding the same
     class does nothing). It returns to 0 when the animation ends, so the next
     press starts from a clean element. */
  const [bounce, setBounce] = useState(0)
  const bouncing = bounce > 0

  /* The hour is sampled on a timer, not once at mount: this panel is a tab
     people leave open overnight, and a pet that went to bed at 20:00 but is
     still standing at 08:05 would be the one thing on the page that lies. A
     minute is fine — the pose has two deadlines a day, not a stopwatch. */
  const [hour, setHour] = useState(() => new Date().getHours())
  useEffect(() => {
    const id = window.setInterval(() => setHour(new Date().getHours()), 60_000)
    return () => window.clearInterval(id)
  }, [])
  const awake = isPandaAwake(hour)

  /* Off duty it naps, and it turns over now and then: one frozen frame for
     eight hours reads as a broken image, not as a sleeping pet. The timer only
     exists while the pet is asleep, and the pose resets on the way back on
     duty so the next night starts from the same frame. */
  const [restStep, setRestStep] = useState(0)
  useEffect(() => {
    if (awake) {
      setRestStep(0)
      return
    }
    const id = window.setInterval(() => setRestStep((n) => n + 1), PANDA_REST_POSE_MS)
    return () => window.clearInterval(id)
  }, [awake])
  const pose = restingPose(restStep)

  /* `is-bouncing` is added on top of whatever pose is showing, never instead
     of it: a poke at night squashes the pet it is actually looking at, and the
     sheet keeps the resting frame. The class order matters in the stylesheet,
     not here. */
  const cls =
    'composer-panda' +
    (awake ? (working ? ' is-working' : '') : ' is-resting') +
    (bouncing ? ' is-bouncing' : '')

  /* The press model (see the header):
       - one press opens the card, after waiting out the double-press window;
       - a second press inside 240ms is a poke;
       - a third press in the same rhythm switches the pet into poking mode:
         from then on every press squashes at once — no window, no card.
     Poking mode is sticky for as long as the taps keep coming and drops 1.2s
     after the last one, so a burst stays a toy while the next deliberate
     click opens the card again.

     While a turn streams the card is opened immediately, before any of that
     bookkeeping: the window exists to tell a poke from a click, and a poke is
     exactly what the working frame cannot take. */
  const tap = useRef<number | null>(null)
  const poking = useRef(false)
  const streak = useRef(0)
  const lastPress = useRef(0)
  const idle = useRef<number | null>(null)
  useEffect(
    () => () => {
      if (tap.current !== null) window.clearTimeout(tap.current)
      if (idle.current !== null) window.clearTimeout(idle.current)
    },
    [],
  )

  /** Squash now, cancelling any parked single press. */
  function squish() {
    if (tap.current !== null) {
      window.clearTimeout(tap.current)
      tap.current = null
    }
    setBounce((n) => n + 1)
  }

  function press() {
    // No second job: this pet is only a toy, and a toy answers at once. Mid-turn
    // the working frame is the answer already, and a squish would replace it.
    if (!onPress) {
      if (!working) setBounce((n) => n + 1)
      return
    }
    // The roster is a live control, not part of the turn: a press while the
    // answer streams toggles the card on the spot. Nothing is parked and no
    // window is waited out — the second press of a double tap has no squish to
    // land on, so it can only mean the same thing the first one did.
    if (working) {
      onPress()
      return
    }
    const now = performance.now()
    streak.current = now - lastPress.current <= 240 ? streak.current + 1 : 1
    lastPress.current = now
    // Third tap in one rhythm: the pet is being played with, so stop making
    // the player wait for a partner press.
    if (streak.current >= 3) poking.current = true
    if (idle.current !== null) window.clearTimeout(idle.current)
    idle.current = window.setTimeout(() => {
      idle.current = null
      streak.current = 0
      poking.current = false
    }, 1200)

    if (poking.current || tap.current !== null) {
      squish()
      return
    }
    tap.current = window.setTimeout(() => {
      tap.current = null
      // A turn that started inside the window does not cancel the click: the
      // card is allowed mid-turn now, and swallowing the press here is exactly
      // the dead-click this used to be reported as.
      onPress()
    }, 240)
  }

  return (
    <button
      type="button"
      class={cls}
      /* Named for what a press does. "Ready" on a button tells you nothing
         about the press; the state is the live region's job, below.
         No `title`: the pet is decoration first, and a native tooltip popped
         a grey bubble over the composer's top-left corner on every pass of
         the pointer. The name still reaches assistive tech through
         `aria-label`, so nothing is lost but the bubble. */
      aria-label={label ?? t('panda.poke')}
      aria-haspopup={onPress ? 'dialog' : undefined}
      aria-expanded={onPress ? !!expanded : undefined}
      onClick={press}
    >
      {/* The stage is the mirror: turning over at night flips it, and the
          transition on `transform` walks it through zero width, which reads as
          the pet rolling onto its other side. The sprite inside keeps its own
          animations, so the two never fight over `transform`. */}
      <span class={'composer-panda-stage' + (!awake && pose === 'flip' ? ' is-flipped' : '')} aria-hidden="true">
        <span
          key={bounce}
          class="composer-panda-sprite"
          onAnimationEnd={() => setBounce(0)}
        />
      </span>
      <span class="composer-panda-label" role="status" aria-live="polite" aria-atomic="true">
        {t(working ? 'panda.working' : awake ? 'panda.ready' : 'panda.resting')}
      </span>
    </button>
  )
}
