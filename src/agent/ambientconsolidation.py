"""
Ambient Memory Consolidation.
Runs on a cron schedule to scan the vector store, identify conflicting
or stale facts, and condense them into clean knowledge graph nodes.
"""
import logging
import asyncio

logger = logging.getLogger(__name__)

class AmbientConsolidator:
    def __init__(self, memory_service, llm_router, dry_run=False):
        self.memory = memory_service
        self.llm_router = llm_router
        self.dry_run = dry_run

    async def consolidate(self):
        """
        Background task to scan the vector store and clean up.
        In a full production scenario, this queries recent memory nodes,
        passes them to an LLM for summarization, and replaces the old vectors.
        """
        logger.info("🧹 Ambient memory consolidation running...")
        
        try:
            # 1. Fetch recent memories
            queue = await asyncio.to_thread(self.memory.get_and_clear_ambient_queue)
            if not queue:
                logger.debug("No recent memories to consolidate.")
                return
            
            # 2. Cluster by session_id
            clusters = {}
            for item in queue:
                session = item.get("metadata", {}).get("session_id", "default")
                clusters.setdefault(session, []).append(item)
                
            llm = self.llm_router.get(tier="standard")
            
            for session, items in clusters.items():
                if len(items) < 2:
                    # Not enough facts to warrant a merge
                    continue
                
                logger.info(f"Consolidating {len(items)} memories for session {session}...")
                
                # 3. LLM call to merge duplicates
                facts_text = "\n".join([f"- {i['content']}" for i in items])
                prompt = (
                    "You are a semantic memory consolidator. "
                    "Review the following chronological facts. "
                    "Condense them into a single coherent summary of facts, removing redundancies. "
                    "If facts conflict, assume the most recent fact overrides the older one.\n\n"
                    f"Facts:\n{facts_text}\n\n"
                    "Provide ONLY the consolidated facts in plain text."
                )
                
                resp = await asyncio.to_thread(llm.invoke, prompt)
                summary = resp.content.strip()
                
                old_ids = [i["id"] for i in items if "id" in i]
                
                # 4. vectorstore.delete(old_ids) and vectorstore.add(new_summaries)
                if self.dry_run:
                    logger.info(f"[DRY RUN] Would delete {len(old_ids)} IDs: {old_ids}")
                    logger.info(f"[DRY RUN] Would store summary: {summary}")
                else:
                    await asyncio.to_thread(self.memory.delete_nodes, old_ids)
                    await asyncio.to_thread(
                        self.memory.store_longterm, 
                        summary, 
                        {"session_id": session, "type": "consolidated_summary"}
                    )
            
            logger.info("Ambient consolidation complete.")
        except Exception as e:
            logger.error(f"Ambient consolidation failed: {e}")

def schedule_consolidation(scheduler, memory_service, llm_router, dry_run=True):
    consolidator = AmbientConsolidator(memory_service, llm_router, dry_run=dry_run)
    # Run every 6 hours to keep the graph optimized
    scheduler.add_job(consolidator.consolidate, "interval", hours=6)
    logger.info("⏰ Ambient consolidation scheduled.")
