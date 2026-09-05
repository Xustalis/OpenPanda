/** Extract a clean project name candidate from an absolute directory path */
export function suggestProjectName(dirPath: string): string {
  if (!dirPath) return ''
  const trimmed = dirPath.replace(/[/\\]+$/, '')
  const parts = trimmed.split(/[/\\]/)
  const last = parts[parts.length - 1] || ''
  return last.replace(/[^\w\u4e00-\u9fa5-]/g, '-').replace(/^-+|-+$/g, '') || 'project'
}
