from datetime import datetime
from typing import List, Dict, Any, Optional
import json
from langchain_core.documents import Document
from .config import (
    POSTGRES_URL, DB_CONNECTION,
    REDIS_URL, KV_STORE_PROVIDER,
    UPSTASH_REDIS_REST_URL, UPSTASH_REDIS_REST_TOKEN,
    VECTOR_STORE_PROVIDER,
    PINECONE_API_KEY, PINECONE_INDEX_NAME, PINECONE_NAMESPACE,
    WEAVIATE_URL, WEAVIATE_API_KEY, WEAVIATE_INDEX_NAME,
)
from .kvstore import create_kv_store
from .vectorstore import create_vector_store


class Memory:
    def __init__(self):
        # --- KV Store (session history, event persistence) ---
        kv_kwargs: dict = {"url": REDIS_URL}
        if KV_STORE_PROVIDER == "upstash":
            kv_kwargs = {"url": UPSTASH_REDIS_REST_URL, "token": UPSTASH_REDIS_REST_TOKEN}
        self.kv = create_kv_store(provider=KV_STORE_PROVIDER, **kv_kwargs)
        # Expose .redis for backward-compatible EventBus wiring in main.py
        self.redis = self.kv

        self._engine = None
        self._embeddings = None
        self._vectorstore = None

    @property
    def engine(self):
        if self._engine is None:
            from sqlalchemy import create_engine
            self._engine = create_engine(POSTGRES_URL)
        return self._engine

    @property
    def embeddings(self):
        if self._embeddings is None:
            from langchain_huggingface import HuggingFaceEmbeddings
            self._embeddings = HuggingFaceEmbeddings(model_name="all-MiniLM-L6-v2")
        return self._embeddings

    @property
    def vectorstore(self):
        if self._vectorstore is None:
            vs_kwargs: dict = {"connection": DB_CONNECTION, "collection_name": "tendril"}
            if VECTOR_STORE_PROVIDER == "pinecone":
                vs_kwargs = {
                    "api_key": PINECONE_API_KEY,
                    "index_name": PINECONE_INDEX_NAME,
                    "namespace": PINECONE_NAMESPACE,
                }
            elif VECTOR_STORE_PROVIDER == "weaviate":
                vs_kwargs = {
                    "url": WEAVIATE_URL,
                    "api_key": WEAVIATE_API_KEY,
                    "index_name": WEAVIATE_INDEX_NAME,
                }
            self._vectorstore = create_vector_store(
                provider=VECTOR_STORE_PROVIDER,
                embeddings=self.embeddings,
                **vs_kwargs,
            )
        return self._vectorstore

    def store_convo(self, session_id: str, role: str, content: str):
        key = f"convo:{session_id}"
        raw = self.kv.get(key)
        history = json.loads(raw or "[]")
        history.append({"role": role, "content": content})
        self.kv.set(key, json.dumps(history[-10:]))
        timestamp = datetime.now().isoformat()
        
        from .eventbus import event_bus, TendrilEvent, generate_run_id
        event_bus.emit(TendrilEvent(
            run_id=generate_run_id(),
            event_type="memory.ingest",
            session_id=session_id,
            data={"role": role, "content": content, "timestamp": timestamp}
        ))

    def get_convo(self, session_id: str) -> List[Dict[str, str]]:
        key = f"convo:{session_id}"
        data = self.kv.get(key)
        return json.loads(data or "[]")

    def get_contextualized_convo(self, session_id: str, current_query: str) -> List[Dict[str, str]]:
        """Fetch conversation history and dynamically inject semantically relevant memories."""
        history = self.get_convo(session_id)
        
        try:
            docs = self.retrieve_relevant(current_query, session_id=session_id, k=3)
            if docs:
                context_str = "Relevant memories from past interactions:\n" + "\n".join([f"- {d.page_content}" for d in docs])
                # Inject right before the user's latest message if history exists
                insert_idx = max(0, len(history) - 1)
                history.insert(insert_idx, {"role": "system", "content": context_str})
        except Exception:
            pass # Fail gracefully if vectorstore is offline or lazy loading fails
            
        return history

    def store_longterm(self, content: str, metadata: Dict[str, Any] = None):
        if metadata is None:
            metadata = {}
        self.vectorstore.add_texts([content], metadatas=[metadata])

    def retrieve_relevant(self, query: str, session_id: Optional[str] = None, k: int = 5) -> List[Document]:
        """Retrieve relevant documents, optionally filtered by session for tenant isolation."""
        filter_dict = {"session_id": session_id} if session_id else None
        return self.vectorstore.similarity_search(query, k=k, filter=filter_dict)
