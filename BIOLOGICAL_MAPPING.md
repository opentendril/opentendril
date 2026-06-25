# Biological Metaphors & OpenTendril Architecture

OpenTendril replaces traditional, generic IT terminology with biological and botanical metaphors. By mimicking evolutionary and natural systems—systems that have spent billions of years optimizing for resilience, modularity, and rapid adaptation—OpenTendril achieves a highly robust and dynamic cognitive architecture. 

This document serves to educate contributors (and non-biologists!) on exactly how these organic concepts map directly to modern LLM engineering paradigms.

## The Cognitive Anatomy

The core execution environment maps to the structural anatomy of a plant.

*   **Stem**: The Go-based orchestrator (`cmd/stem`). Just like a physical plant stem transports nutrients and structurally supports the plant, the Go Stem handles the HTTP networking, routing, and fundamental support structure for the AI.
*   **Sprout**: The ephemeral Docker sandbox. A sprout is a brand new, isolated shoot of growth. In OpenTendril, every time a task executes, a fresh container (the Sprout) is created, providing a clean, isolated environment.
*   **Tendril**: The autonomous AI agent (the Python loop) running inside the Sprout. In nature, a tendril is a specialized stem/leaf that autonomously reaches out, senses its environment, and grasps objects to pull the plant forward. In our system, the Tendril autonomously reads files, runs commands, and accomplishes the user's task.
*   **Hormonal Triggers**: The pre-execution security gates. Plants use hormones (like auxins) to instantly trigger or halt growth based on environmental stimuli (light, gravity, damage). In OpenTendril, Hormonal Triggers are lightweight bash scripts that intercept tasks and can instantly "block growth" (abort execution) if a threat is detected.

## The Genetic Prompt Hierarchy

To scale our prompt engineering dynamically, OpenTendril maps prompt layers to genetics.

*   **Genotype (Base Model Identity):** The core DNA of the AI. A Genotype is the foundational system prompt defining the overall identity, behavioral constraints, and role of the Tendril (e.g., "You are a Senior Go Engineer"). It is the fundamental blueprint. *(Common IT term: Persona or System Prompt)*.
*   **Plasmid (Modular Skill Injection):** In microbiology, a plasmid is a small, modular packet of DNA that can be transferred between cells to instantly grant them new traits (like antibiotic resistance). In OpenTendril, a Plasmid is a reusable, modular block of context or tools injected into a Genotype on the fly (e.g., "Here is the syntax documentation for React.js"). *(Common IT term: RAG context block or Tool definition)*.
*   **Transcript (Task Execution):** In biology, RNA transcription is the process of copying genetic instructions into a transient format (mRNA) that the cell immediately executes to perform an action. In OpenTendril, the Transcript is the one-off, contextual prompt fed to the Tendril for a single execution run (e.g., "Refactor this file"). *(Common IT term: User Prompt or Task Instruction)*.
*   **Sequence (Workflow Automation):** A defined genetic sequence dictating a complex chain of events. In OpenTendril, a Sequence is a predefined workflow that chains multiple Tendrils together (e.g., A Frontend Genotype writes the code, then a Testing Genotype reviews it). *(Common IT term: Agentic Pipeline or Workflow)*.

## Architectural Flow

```mermaid
graph TD
    subgraph The Go Stem
        API[Incoming Request] --> HT[Hormonal Triggers]
        HT -- Growth Blocked --> Abort
        HT -- Growth Allowed --> OS[Sprout Spawner]
    end

    subgraph Ephemeral Sprout (Docker Sandbox)
        OS --> T[The Tendril AI]
        
        subgraph Genetic Injection
            G[Genotype: Core Persona] --> T
            P1[Plasmid: Skill A] -.-> T
            P2[Plasmid: Skill B] -.-> T
            TR[Transcript: Task Instructions] --> T
        end
        
        T --> Env[Isolated Workspace]
    end

    classDef biological fill:#e8f5e9,stroke:#2e7d32,stroke-width:2px;
    classDef stem fill:#e0f7fa,stroke:#006064,stroke-width:2px;
    classDef sprout fill:#fff3e0,stroke:#e65100,stroke-width:2px;
    
    class G,P1,P2,TR,HT,T biological;
    class API,OS stem;
    class Env sprout;
```
