import { useConnection } from "./state/connection";
import { Onboarding } from "./components/Onboarding";
import { CommandCenter } from "./components/CommandCenter";

export function App() {
  const configured = useConnection((s) => s.configured);
  return configured ? <CommandCenter /> : <Onboarding />;
}
