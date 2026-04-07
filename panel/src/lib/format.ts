import { formatDistanceToNow, format } from 'date-fns'

/** Format ISO date string to relative time (e.g., "3 minutes ago") */
export function timeAgo(iso: string): string {
  try {
    return formatDistanceToNow(new Date(iso), { addSuffix: true })
  } catch {
    return iso
  }
}

/** Format ISO date string to human-readable format */
export function formatDate(iso: string, fmt = 'MMM d, yyyy HH:mm'): string {
  try {
    return format(new Date(iso), fmt)
  } catch {
    return iso
  }
}

/** Truncate a validator address: g1abcd...wxyz */
export function truncateAddr(addr: string, chars = 6): string {
  if (addr.length <= chars * 2 + 3) return addr
  return `${addr.slice(0, chars)}...${addr.slice(-chars)}`
}

/** Copy text to clipboard with fallback */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

/** Format a threshold key into a nice label: "warning_threshold" → "Warning Threshold" */
export function formatThresholdLabel(key: string): string {
  return key
    .split('_')
    .map(w => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

/** Get the unit suffix for a threshold key */
export function getThresholdUnit(key: string): string {
  if (key.includes('minutes')) return 'min'
  if (key.includes('seconds')) return 'sec'
  if (key.includes('hours')) return 'h'
  if (key.includes('days')) return 'days'
  if (key.includes('threshold')) return 'blocks'
  return ''
}

/** Get level badge CSS class */
export function levelBadgeClass(level: string): string {
  switch (level.toUpperCase()) {
    case 'CRITICAL': return 'badge-critical'
    case 'WARNING': return 'badge-warn'
    case 'RESOLVED': return 'badge-ok'
    case 'MUTED': return 'badge-muted'
    case 'INFO': return 'badge-info'
    default: return 'badge-muted'
  }
}
