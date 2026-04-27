"""
src/vectorstore.py — Provider-agnostic Vector Store factory.

Returns a LangChain-compatible VectorStore instance for the configured backend.
All backends share the same interface (add_texts, similarity_search) so
memory.py requires no changes beyond calling this factory.

Backends:
  pgvector  — Default. Local Postgres + pgvector extension. Zero added cost.
  pinecone  — Managed cloud vector DB. Scales to billions of vectors.
              Best for: large codebases, multi-tenant SaaS, cross-session memory.
  weaviate  — Open-source managed vector DB. Self-hostable or cloud.
              Best for: hybrid keyword+vector search, fine-grained metadata.

Configuration (.env):
  VECTOR_STORE_PROVIDER=pgvector | pinecone | weaviate

  # Pinecone
  PINECONE_API_KEY=...
  PINECONE_INDEX_NAME=tendril

  # Weaviate
  WEAVIATE_URL=http://weaviate:8080
  WEAVIATE_API_KEY=...           # optional for local Weaviate

Why external vector stores matter (Issue #8 rationale):
  pgvector is excellent for local use but has limits in managed SaaS:
  - Pinecone: metadata filtering, multi-namespace tenant isolation, serverless pricing
  - Weaviate: hybrid BM25+vector search, schema-aware retrieval for structured code
"""

import logging
from langchain_core.embeddings import Embeddings
from langchain_core.vectorstores import VectorStore

logger = logging.getLogger(__name__)


def create_vector_store(
    provider: str,
    embeddings: Embeddings,
    **kwargs,
) -> VectorStore:
    """
    Instantiate the appropriate LangChain VectorStore.

    Args:
        provider:   "pgvector" | "pinecone" | "weaviate"
        embeddings: Embedding model instance (shared across backends)
        **kwargs:   Provider-specific config — see below

    Returns:
        A LangChain VectorStore ready for add_texts() and similarity_search()

    Kwargs by provider:
        pgvector:  connection (str), collection_name (str)
        pinecone:  api_key (str), index_name (str), namespace (str, optional)
        weaviate:  url (str), api_key (str, optional), index_name (str)
    """
    if provider == "pinecone":
        return _build_pinecone(embeddings, **kwargs)
    if provider == "weaviate":
        return _build_weaviate(embeddings, **kwargs)
    # Default: pgvector
    return _build_pgvector(embeddings, **kwargs)


# ---------------------------------------------------------------------------
# Backend builders
# ---------------------------------------------------------------------------

def _build_pgvector(embeddings: Embeddings, **kwargs) -> VectorStore:
    """Local Postgres + pgvector — the zero-dependency default."""
    from langchain_postgres.vectorstores import PGVector
    connection = kwargs.get("connection", "")
    collection = kwargs.get("collection_name", "tendril")
    if not connection:
        raise ValueError("pgvector requires a 'connection' string (POSTGRES_URL)")
    store = PGVector(
        connection=connection,
        embeddings=embeddings,
        collection_name=collection,
    )
    logger.info(f"📦 VectorStore: pgvector backend (collection='{collection}')")
    return store


def _build_pinecone(embeddings: Embeddings, **kwargs) -> VectorStore:
    """
    Pinecone managed vector DB.

    Requires: langchain-pinecone  (pip install langchain-pinecone)
    """
    try:
        from langchain_pinecone import PineconeVectorStore
        from pinecone import Pinecone
    except ImportError as exc:
        raise ImportError(
            "Pinecone support requires: pip install langchain-pinecone pinecone-client"
        ) from exc

    api_key = kwargs.get("api_key", "")
    index_name = kwargs.get("index_name", "tendril")
    namespace = kwargs.get("namespace", "")

    if not api_key:
        raise ValueError("Pinecone requires PINECONE_API_KEY")

    pc = Pinecone(api_key=api_key)
    index = pc.Index(index_name)
    store = PineconeVectorStore(index=index, embedding=embeddings, namespace=namespace)
    logger.info(f"📦 VectorStore: Pinecone backend (index='{index_name}', ns='{namespace}')")
    return store


def _build_weaviate(embeddings: Embeddings, **kwargs) -> VectorStore:
    """
    Weaviate vector DB — self-hostable or Weaviate Cloud.

    Requires: langchain-weaviate  (pip install langchain-weaviate)
    """
    try:
        import weaviate
        from langchain_weaviate import WeaviateVectorStore
    except ImportError as exc:
        raise ImportError(
            "Weaviate support requires: pip install langchain-weaviate weaviate-client"
        ) from exc

    url = kwargs.get("url", "http://weaviate:8080")
    api_key = kwargs.get("api_key", "")
    index_name = kwargs.get("index_name", "Tendril")

    auth = weaviate.auth.AuthApiKey(api_key) if api_key else None
    client = weaviate.connect_to_custom(
        http_host=url.split("://")[-1].split(":")[0],
        http_port=int(url.split(":")[-1]) if ":" in url.split("://")[-1] else 8080,
        http_secure=url.startswith("https"),
        auth_credentials=auth,
    )
    store = WeaviateVectorStore(client=client, index_name=index_name, text_key="text", embedding=embeddings)
    logger.info(f"📦 VectorStore: Weaviate backend (url='{url}', index='{index_name}')")
    return store
