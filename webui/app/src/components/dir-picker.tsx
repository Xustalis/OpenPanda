import { useEffect, useState } from 'preact/hooks'
import { api, type DirectoryListing } from '../api/client'
import { t } from '../i18n'
import { suggestProjectName } from './dir-utils'

export interface DirPickerProps {
  value?: string
  onChange: (path: string) => void
  onSuggestName?: (name: string) => void
  placeholder?: string
  disabled?: boolean
}

export function DirPicker({
  value = '',
  onChange,
  onSuggestName,
  placeholder,
  disabled = false,
}: DirPickerProps) {
  const [modalOpen, setModalOpen] = useState(false)
  const [loading, setLoading] = useState(false)

  async function handlePick() {
    if (disabled || loading) return
    setLoading(true)
    try {
      const res = await api.chooseDirectory(value)
      if (res && res.path && !res.canceled) {
        onChange(res.path)
        if (onSuggestName) {
          onSuggestName(suggestProjectName(res.path))
        }
        return
      }
      if (res && res.error) {
        // Native dialog not supported or failed — fall back to interactive modal browser
        setModalOpen(true)
      }
    } catch {
      setModalOpen(true)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div class="dir-picker-wrap">
      <div class={`dir-picker-box ${value ? 'has-value' : ''} ${disabled ? 'disabled' : ''}`}>
        <span class="dir-picker-icon" aria-hidden="true">📁</span>
        <span class="dir-picker-path" title={value || placeholder || t('projects.dirPlaceholder')}>
          {value || <span class="dim">{placeholder || t('projects.dirPlaceholder')}</span>}
        </span>
        {value && !disabled && (
          <button
            type="button"
            class="dir-picker-clear"
            onClick={(e) => {
              e.stopPropagation()
              onChange('')
            }}
            title={t('common.clear')}
          >
            ✕
          </button>
        )}
        <button
          type="button"
          class="btn dir-picker-browse-btn"
          onClick={handlePick}
          disabled={disabled || loading}
        >
          {loading ? t('common.loading') : t('projects.chooseDir')}
        </button>
      </div>

      {modalOpen && (
        <DirBrowserModal
          initialPath={value}
          onSelect={(selectedPath) => {
            onChange(selectedPath)
            if (onSuggestName) {
              onSuggestName(suggestProjectName(selectedPath))
            }
            setModalOpen(false)
          }}
          onClose={() => setModalOpen(false)}
        />
      )}
    </div>
  )
}

function DirBrowserModal({
  initialPath,
  onSelect,
  onClose,
}: {
  initialPath?: string
  onSelect: (path: string) => void
  onClose: () => void
}) {
  const [currentPath, setCurrentPath] = useState(initialPath || '')
  const [listing, setListing] = useState<DirectoryListing | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  async function loadPath(path: string) {
    setLoading(true)
    setError('')
    try {
      const res = await api.listDirectories(path)
      setListing(res)
      setCurrentPath(res.current)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadPath(currentPath)
  }, [])

  const parts = currentPath ? currentPath.split(/[/\\]/).filter(Boolean) : []
  const isWindows = listing?.separator === '\\'

  return (
    <div
      class="modal-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div class="modal dir-browser-modal" role="dialog" aria-modal="true">
        <div class="dir-browser-header">
          <h2 class="modal-title">{t('projects.selectDirTitle')}</h2>
          <button type="button" class="btn-icon" onClick={onClose} title={t('common.close')}>
            ✕
          </button>
        </div>

        <div class="dir-breadcrumbs">
          <button
            type="button"
            class="dir-crumb-btn"
            onClick={() => loadPath(isWindows ? 'C:\\' : '/')}
          >
            {isWindows ? 'C:\\' : '/'}
          </button>
          {parts.map((p, i) => {
            const pathUpTo = (isWindows ? '' : '/') + parts.slice(0, i + 1).join(isWindows ? '\\' : '/')
            return (
              <span key={i} class="dir-crumb-item">
                <span class="dir-crumb-sep">/</span>
                <button
                  type="button"
                  class={`dir-crumb-btn ${i === parts.length - 1 ? 'active' : ''}`}
                  onClick={() => loadPath(pathUpTo)}
                >
                  {p}
                </button>
              </span>
            )
          })}
        </div>

        {listing?.parent && listing.parent !== listing.current && (
          <div class="dir-up-row">
            <button
              type="button"
              class="dir-entry-btn up"
              onClick={() => loadPath(listing.parent)}
            >
              <span class="dir-icon">⬆️</span>
              <span>.. ({t('projects.parentDir')})</span>
            </button>
          </div>
        )}

        <div class="dir-entries-list">
          {loading ? (
            <div class="dim dir-loading">
              <span class="spinner spinner-inline" aria-hidden="true" />
              {t('common.loading')}
            </div>
          ) : error ? (
            <div class="dir-error">{error}</div>
          ) : listing?.entries && listing.entries.length > 0 ? (
            listing.entries.map((entry) => (
              <button
                key={entry.path}
                type="button"
                class="dir-entry-btn"
                onClick={() => loadPath(entry.path)}
              >
                <span class="dir-icon">📁</span>
                <span class="dir-entry-name">{entry.name}</span>
              </button>
            ))
          ) : (
            <div class="dim dir-empty">{t('projects.noSubdirs')}</div>
          )}
        </div>

        <div class="dir-browser-footer">
          <div class="dir-current-selected">
            <span class="dim">{t('projects.currentPath')}: </span>
            <code>{currentPath || '/'}</code>
          </div>
          <div class="modal-actions">
            <button type="button" class="btn" onClick={onClose}>
              {t('common.cancel')}
            </button>
            <button
              type="button"
              class="btn primary"
              disabled={!currentPath}
              onClick={() => onSelect(currentPath)}
            >
              {t('projects.confirmSelect')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
