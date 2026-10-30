client<llm> BedrockNovaMicro {
  provider aws-bedrock
  options {
    finish_reason_deny_list ["max_tokens"]
    model "amazon.nova-micro-v1:0"
    inference_configuration {
      max_tokens 2048
      temperature 0.1
    }
  }
}
