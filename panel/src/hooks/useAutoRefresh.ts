import { useEffect, useRef } from 'react'

/**
 * Auto-refresh hook — calls `callback` every `intervalMs` milliseconds.
 * Cleans up on unmount. Pauses when `enabled` is false.
 */
export function useAutoRefresh(callback: () => void, intervalMs: number, enabled = true) {
  const savedCallback = useRef(callback)

  useEffect(() => {
    savedCallback.current = callback
  }, [callback])

  useEffect(() => {
    if (!enabled) return
    const id = setInterval(() => savedCallback.current(), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs, enabled])
}
