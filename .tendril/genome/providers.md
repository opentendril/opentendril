# LLM Providers

OpenTendril resolves LLM providers from environment variables first, then `.tendril/config.yaml`, then built-in defaults.

## Local

The `local` provider targets any OpenAI-compatible local server, including Ollama, llama.cpp, and LM Studio. It does not require an API key.

Default configuration:

```yaml
llm:
  default-provider: local
  providers:
    local:
      base-url: http://host.docker.internal:11434/v1
      model: llama3.2
      endpoint: /chat/completions
      temperature: 0.1
```

Environment overrides:

```sh
DEFAULT_LLM_PROVIDER=local
LOCAL_INFERENCE_URL=http://localhost:11434/v1
LOCAL_MODEL_NAME=llama3.2
```

CLI checks:

```sh
tendril llm list --url http://localhost:11434/v1
tendril llm test --url http://localhost:11434/v1 --model llama3.2 --prompt "Say hello from local inference."
```

For Docker-hosted Tendril processes, `http://host.docker.internal:11434/v1` lets the container reach a host Ollama server. For direct host execution, `http://localhost:11434/v1` is usually appropriate.
