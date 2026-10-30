client<llm> BedrockNovaMicro {
  provider aws-bedrock
  retry_policy BedrockRetry
  options {
    finish_reason_deny_list ["max_tokens"]
    model "amazon.nova-micro-v1:0"
    inference_configuration {
      max_tokens 2048
      temperature 0.1
    }
  }
}

retry_policy BedrockRetry {
  max_retries 2
  strategy {
    type exponential_backoff
    delay_ms 500
    multiplier 2.0
    max_delay_ms 10000
  }
}
