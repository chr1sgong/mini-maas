T0=$(date +%s)
python bench/bench.py --base-url http://localhost:8080/v1 --api-key sk-tenant-a-EXAMPLE --model qwen2.5-7b-awq \
  --mode steady --concurrency 32 --duration 300 --out /tmp/d10-count.jsonl
sleep 20          # 等最慢的 15s scrape 代际 settle，否则末段请求还没进
T2=$(date +%s); echo "窗口: $T0 → $T2"