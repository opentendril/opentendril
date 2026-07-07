// Phenotypic selection rendered as a competition arena: each generation's
// phenotype pods orbit the branch tip; on evaluation the survivors stay lit
// while the withered fall and compost; on completion the AlphaPhenotype
// crowns gold with its fitness score.

import type { SelectionArena as ArenaModel } from "../../state/garden";

const RING = 40;

export function SelectionArena({
  arena,
  center,
  side,
}: {
  arena: ArenaModel;
  center: { x: number; y: number };
  side: 1 | -1;
}) {
  const pods = arena.pods;
  const label =
    arena.phase === "complete"
      ? `alpha ${arena.alphaScore ?? ""}`
      : arena.phase === "evaluated"
        ? `gen ${arena.generation} · ${arena.survivors} survive`
        : arena.phase === "generation"
          ? `gen ${arena.generation} growing`
          : `selection · pop ${arena.populationSize}`;

  return (
    <g>
      {/* arena ring */}
      <circle
        cx={center.x}
        cy={center.y}
        r={RING}
        fill="none"
        stroke={arena.phase === "complete" ? "var(--bloom)" : "var(--glass-border-bright)"}
        strokeDasharray={arena.phase === "complete" ? undefined : "3 5"}
        strokeWidth="1"
        opacity={arena.phase === "complete" ? 0.55 : 0.8}
        className={arena.phase === "generation" ? "pulse-node" : undefined}
      />

      {pods.map((pod, i) => {
        const angle = (i / Math.max(pods.length, 1)) * Math.PI * 2 - Math.PI / 2;
        const x = center.x + Math.cos(angle) * RING;
        const y = center.y + Math.sin(angle) * RING;

        if (pod.status === "withered") {
          return (
            <g key={pod.id} className="pod-withered">
              <circle cx={x} cy={y} r="4" fill="var(--wither-dim)" />
            </g>
          );
        }
        if (pod.status === "alpha") {
          return (
            <g key={pod.id} className="bud-in alpha-glow">
              <circle cx={x} cy={y} r="6.5" fill="var(--bloom)" />
              <circle cx={x} cy={y} r="2.4" fill="#fff6dd" />
            </g>
          );
        }
        if (pod.status === "survived") {
          return (
            <g key={pod.id} className="chloro-glow">
              <circle cx={x} cy={y} r="4.6" fill="var(--chlorophyll)" />
            </g>
          );
        }
        // growing
        return (
          <circle
            key={pod.id}
            className="bud-in"
            cx={x}
            cy={y}
            r="3.6"
            fill="var(--sap)"
            opacity="0.85"
          />
        );
      })}

      <text
        x={center.x + side * 4}
        y={center.y - RING - 8}
        textAnchor="middle"
        fontSize="9"
        fontFamily="var(--font-mono)"
        fill={arena.phase === "complete" ? "var(--bloom)" : "var(--ink-mute)"}
      >
        {label}
      </text>
      {arena.phase === "evaluated" && typeof arena.bestScore === "number" ? (
        <text
          x={center.x + side * 4}
          y={center.y + RING + 14}
          textAnchor="middle"
          fontSize="9"
          fontFamily="var(--font-mono)"
          fill="var(--sap)"
        >
          best {arena.bestScore}
        </text>
      ) : null}
    </g>
  );
}
