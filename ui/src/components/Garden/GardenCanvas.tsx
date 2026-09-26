import { useStem } from "../../state/store";
import { PlantFigure } from "./PlantFigure";
import { TendrilMark } from "../TendrilMark";

export function GardenCanvas() {
  const garden = useStem((s) => s.garden);
  const plants = Object.values(garden.plants).sort((a, b) => a.bornAt - b.bornAt);

  return (
    <section className="garden glass" aria-label="Living orchestration garden">
      <div className="garden-soil" />
      {plants.length === 0 ? (
        <div className="garden-empty">
          <div className="seed">
            <TendrilMark className="brand-mark" />
          </div>
          <h3>The garden is dormant</h3>
          <p>
            When the Stem orchestrates, parallel sprouting and phenotypic
            selection grow here in real time. Start a Seed in the workbench to
            plant the first tendril.
          </p>
        </div>
      ) : (
        <div className="garden-scroll">
          {plants.map((plant) => (
            <PlantFigure key={plant.key} plant={plant} />
          ))}
        </div>
      )}
    </section>
  );
}
