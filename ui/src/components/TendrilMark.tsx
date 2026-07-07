/** The OpenTendril mark: a coiling tendril rising from the rhizome line. */
export function TendrilMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={className} aria-hidden="true">
      <path
        d="M4 29 H28"
        stroke="var(--wither)"
        strokeWidth="1.4"
        strokeLinecap="round"
        opacity="0.7"
      />
      <path
        d="M16 29 C15 22, 17 18, 16 13 C15.2 8.6, 18 5.4, 21.4 6.4 C24.4 7.3, 24.6 11, 22 12.2 C19.9 13.2, 18 11.6, 18.6 9.6"
        fill="none"
        stroke="var(--chlorophyll)"
        strokeWidth="2"
        strokeLinecap="round"
      />
      <path
        d="M16 20 C12.8 19.4, 10.6 17, 10.2 14.2"
        fill="none"
        stroke="var(--sap)"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
      <circle cx="10.2" cy="14.2" r="1.6" fill="var(--bloom)" />
    </svg>
  );
}
