// One orchestration rendered as a living plant. Pure SVG + CSS animation:
// the stem rises as steps (branches) appear, parallel sprouts unfurl as
// tendril tips, MycelialMerge pulls glowing filaments back into the branch
// node, and a phenotypic-selection step grows an arena of competing pods.

import type { Branch, Plant, TendrilTip } from "../../state/garden";
import { SelectionArena } from "./SelectionArena";

const W = 250;
const H = 400;
const GROUND = H - 34;
const CX = W / 2;
const BRANCH_GAP = 78;
const FIRST_BRANCH = 92;

function stemTopFor(plant: Plant): number {
  const reach = FIRST_BRANCH + Math.max(plant.branches.length - 1, 0) * BRANCH_GAP + 34;
  return Math.max(GROUND - reach, 26);
}

function branchAnchorY(order: number): number {
  return GROUND - FIRST_BRANCH - order * BRANCH_GAP;
}

interface BranchGeometry {
  side: 1 | -1;
  anchor: { x: number; y: number };
  end: { x: number; y: number };
  path: string;
}

function branchGeometry(branch: Branch): BranchGeometry {
  const side = (branch.order % 2 === 0 ? 1 : -1) as 1 | -1;
  const anchor = { x: CX, y: branchAnchorY(branch.order) };
  const end = { x: CX + side * 74, y: anchor.y - 30 };
  const path = `M ${anchor.x} ${anchor.y} C ${anchor.x + side * 26} ${anchor.y - 2}, ${
    end.x - side * 28
  } ${end.y + 18}, ${end.x} ${end.y}`;
  return { side, anchor, end, path };
}

function tipGeometry(geo: BranchGeometry, index: number, count: number) {
  const spread = Math.min(count, 6);
  const t = count <= 1 ? 0.5 : index / (count - 1);
  const angle = (-118 + t * 92) * (Math.PI / 180); // fan above the branch end
  const reach = 30 + (index % 3) * 7 + Math.min(spread, 4);
  const x = geo.end.x + Math.cos(angle) * reach * geo.side;
  const y = geo.end.y + Math.sin(angle) * reach;
  const midX = geo.end.x + Math.cos(angle) * reach * 0.45 * geo.side + geo.side * 6;
  const midY = geo.end.y + Math.sin(angle) * reach * 0.5 + 4;
  return { x, y, path: `M ${geo.end.x} ${geo.end.y} Q ${midX} ${midY}, ${x} ${y}` };
}

function TipFigure({ tip, geo, index, count }: {
  tip: TendrilTip;
  geo: BranchGeometry;
  index: number;
  count: number;
}) {
  const { x, y, path } = tipGeometry(geo, index, count);

  if (tip.status === "withered") {
    return (
      <g className="tip-withered">
        <path d={path} fill="none" stroke="var(--wither)" strokeWidth="1.4" strokeLinecap="round" />
        <circle cx={x} cy={y} r="3" fill="var(--wither-dim)" />
      </g>
    );
  }

  if (tip.status === "matured") {
    return (
      <g>
        <path d={path} fill="none" stroke="var(--chlorophyll-dim)" strokeWidth="1.6" strokeLinecap="round" />
        <g className="bud-in bloom-glow">
          <circle cx={x} cy={y} r="4.4" fill="var(--bloom)" />
          <circle cx={x} cy={y} r="1.7" fill="#fff6dd" />
        </g>
      </g>
    );
  }

  // emerging: still unfurling
  return (
    <g>
      <path
        d={path}
        className="grow-in"
        fill="none"
        stroke="var(--chlorophyll)"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
      <circle cx={x} cy={y} r="3.2" fill="var(--chlorophyll)" className="bud-in chloro-glow" />
    </g>
  );
}

function BranchFigure({ branch, streaming }: { branch: Branch; streaming: boolean }) {
  const geo = branchGeometry(branch);
  const merging = branch.mergePhase === "reduce";
  const merged = branch.mergePhase === "complete";
  const tips = branch.tips;

  return (
    <g>
      <path
        d={geo.path}
        className="grow-in"
        fill="none"
        stroke={branch.failed ? "var(--scorch)" : "var(--chlorophyll-dim)"}
        strokeWidth="2.4"
        strokeLinecap="round"
      />
      {streaming ? (
        <path
          d={geo.path}
          className="sap-flow"
          fill="none"
          stroke="var(--sap)"
          strokeWidth="1.2"
          strokeLinecap="round"
          opacity="0.9"
        />
      ) : null}

      {/* budding placeholders announced by parallel-sprouting(map) before
          each sprout-emerged arrives */}
      {branch.expectedSprouts > tips.length
        ? Array.from({ length: branch.expectedSprouts - tips.length }, (_, i) => {
            const g = tipGeometry(geo, tips.length + i, Math.max(branch.expectedSprouts, 1));
            return (
              <circle
                key={`bud-${i}`}
                cx={g.x}
                cy={g.y}
                r="2"
                fill="none"
                stroke="var(--ink-faint)"
                strokeDasharray="2 2"
              />
            );
          })
        : null}

      {tips.map((tip, i) => (
        <g key={tip.index}>
          {/* MycelialMerge: matured tips send filaments back to the node */}
          {(merging || merged) && tip.status === "matured" ? (
            <path
              d={tipGeometry(geo, i, Math.max(tips.length, 1)).path}
              className={merging ? "merge-filament" : undefined}
              fill="none"
              stroke="var(--spore)"
              strokeWidth="1"
              opacity={merged ? 0.5 : 0.9}
            />
          ) : null}
          <TipFigure tip={tip} geo={geo} index={i} count={Math.max(tips.length, 1)} />
        </g>
      ))}

      {/* branch node: pulses while the merge consensus is being grown */}
      <circle
        cx={geo.anchor.x}
        cy={geo.anchor.y}
        r={merged ? 4.4 : 3.4}
        fill={branch.failed ? "var(--scorch)" : merged ? "var(--spore)" : "var(--chlorophyll)"}
        className={merging ? "pulse-node" : merged ? "chloro-glow" : undefined}
      />

      {branch.arena ? <SelectionArena arena={branch.arena} center={geo.end} side={geo.side} /> : null}
    </g>
  );
}

export function PlantFigure({ plant }: { plant: Plant }) {
  const top = stemTopFor(plant);
  const streamingBranch = plant.streamingStepId;
  const stemPath = `M ${CX} ${GROUND} C ${CX - 9} ${GROUND - 60}, ${CX + 9} ${top + 80}, ${CX} ${top}`;

  const state = plant.failed
    ? { text: "withered", cls: "bad" }
    : plant.complete
      ? { text: "matured", cls: "ok" }
      : plant.streamingStepId
        ? { text: "streaming", cls: "ok" }
        : { text: "growing", cls: "" };

  return (
    <div className="plant-figure" style={{ width: W, height: H }}>
      <svg viewBox={`0 0 ${W} ${H}`} width={W} height={H}>
        {/* rhizome line + root ripples */}
        <line x1="10" y1={GROUND} x2={W - 10} y2={GROUND} stroke="var(--wither-dim)" strokeWidth="1" opacity="0.8" />
        <path
          d={`M ${CX} ${GROUND} q -18 10 -34 13 M ${CX} ${GROUND} q 16 9 32 14 M ${CX} ${GROUND} q -4 12 -2 18`}
          fill="none"
          stroke="var(--wither-dim)"
          strokeWidth="1"
          opacity="0.65"
        />

        <g className="sway">
          {/* stem */}
          <path
            d={stemPath}
            className="grow-in"
            fill="none"
            stroke={plant.failed ? "var(--wither)" : "var(--chlorophyll-dim)"}
            strokeWidth="3"
            strokeLinecap="round"
          />
          {streamingBranch ? (
            <path
              d={stemPath}
              className="sap-flow"
              fill="none"
              stroke="var(--sap)"
              strokeWidth="1.3"
              strokeLinecap="round"
              opacity="0.85"
            />
          ) : null}

          {/* crown */}
          <circle
            cx={CX}
            cy={top}
            r={plant.complete ? 5 : 3.6}
            fill={plant.failed ? "var(--scorch)" : plant.complete ? "var(--bloom)" : "var(--sap)"}
            className={plant.complete ? "bloom-glow bud-in" : streamingBranch ? "pulse-node" : undefined}
          />

          {plant.branches.map((branch) => (
            <BranchFigure
              key={branch.stepId}
              branch={branch}
              streaming={streamingBranch === branch.stepId}
            />
          ))}
        </g>
      </svg>

      {plant.whispers.map((w, i) => (
        <div
          key={w.id}
          className="whisper"
          style={{ top: 12 + i * 52, left: 8, right: 8, maxWidth: "unset" }}
        >
          {w.text}
        </div>
      ))}

      <div className="plant-label">
        {plant.label} <span className={`state ${state.cls}`}>· {state.text}</span>
      </div>
    </div>
  );
}
