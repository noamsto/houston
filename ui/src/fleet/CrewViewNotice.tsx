export function CrewViewNotice({ notices }: { notices: string[] }) {
  const unique = [...new Set(notices)]
  if (unique.length === 0) return null
  return (
    <div className="crew-view-notice" role="status">
      {unique.map((n) => (
        <div key={n}>{n}</div>
      ))}
    </div>
  )
}
