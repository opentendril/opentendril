from datetime import datetime
from typing import List, Dict, Any, Optional
import json
from sqlalchemy import create_engine
from langchain_huggingface import HuggingFaceEmbeddings
from langchain_postgres.vectorstores import PGVector
from langchain_core.documents import Document
from .config import (
    POSTGRES_URL, DB_CONNECTION,
    REDIS_URL, KV_STORE_PROVIDER,
    UPSTASH_REDIS_REST_URL, UPSTASH_REDIS_REST_TOKEN,
)
from .kvstore import create_kv_store


class Memory:
    def __init__(self):
        kv_kwargs: dict = {"url": REDIS_URL}
        if KV_STORE_PROVIDER == "upstash":
            kv_kwargs = {"url": UPSTASH_REDIS_REST_URL, "token": UPSTASH_REDIS_REST_TOKEN}
        self.kv = create_kv_store(provider=KV_STORE_PROVIDER, **kv_kwargs)
        # Expose .redis for backward-compatible EventBus wiring in main.py
        self.redis = self.kv

        self.engine = create_engine(POSTGRES_URL)
        self.embeddings = HuggingFaceEmbeddings(model_name="all-MiniLM-L6-v2")
        self.vectorstore = PGVector(
            connection=DB_CONNECTION,
            embeddings=self.embeddings,
            collection_name="tendril",
        )

    def store_convo(self, session_id: str, role: str, content: str):
        key = f"convo:{session_id}"
        raw = self.kv.get(key)
        history = json.loads(raw or "[]")
        history.append({"role": role, "content": content})
        self.kv.set(key, json.dumps(history[-10:]))
        timestamp = datetime.now().isoformat()
        self.store_longterm(f"{role}: {content}", {"session_id": session_id, "timestamp": timestamp})

    def get_convo(self, session_id: str) -> List[Dict[str, str]]:
        key = f"convo:{session_id}"
        data = self.kv.get(key)
        return json.loads(data or "[]")

    def store_longterm(self, content: str, metadata: Dict[str, Any] = None):
        if metadata is None:
            metadata = {}
        self.vectorstore.add_texts([content], metadatas=[metadata])

    def retrieve_relevant(self, query: str, session_id: Optional[str] = None, k: int = 5) -> List[Document]:
        """Retrieve relevant documents, optionally filtered by session for tenant isolation."""
        filter_dict = {"session_id": session_id} if session_id else None
        return self.vectorstore.similarity_search(query, k=k, filter=filter_dict)
