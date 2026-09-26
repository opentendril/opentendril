// Presentation of the recorded Terrarium provider. The value is an observed
// fact. Configuration is never consulted, and providers other than host do
// not gain an isolation claim beyond the recorded identity.

export interface TerrariumProviderFact {
  recorded?: string;
  label: string;
  note?: string;
  known: boolean;
}

export function terrariumProviderFact(value?: string): TerrariumProviderFact {
  const recorded = value?.trim() ?? "";
  if (!recorded) {
    return { label: "Unknown", known: false };
  }
  switch (recorded) {
    case "docker":
      return { recorded, label: "Docker", known: true };
    case "gvisor":
      return { recorded, label: "gVisor", known: true };
    case "firecracker":
      return { recorded, label: "Firecracker", known: true };
    case "host":
      return {
        recorded,
        label: "Host execution",
        note: "Host execution bypasses Terrarium isolation.",
        known: true,
      };
    default:
      return { recorded, label: recorded, known: true };
  }
}
