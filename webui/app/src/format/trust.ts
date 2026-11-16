// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'

/** Keep protocol values stable while showing the permission they grant. */
export function trustLabel(value?: string): string {
  const level = value || 'mesh'
  return t('nodes.trust.' + level, level)
}
