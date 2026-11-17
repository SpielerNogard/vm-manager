if vmm_linear_key=$(curl -fsS --max-time 1 "${VMM_TOKENS_URL:-http://10.201.0.1:9002}/v1/linear/token" 2>/dev/null) && [ -n "$vmm_linear_key" ]; then
  LINEAR_API_KEY=$vmm_linear_key
  export LINEAR_API_KEY
fi
unset vmm_linear_key
