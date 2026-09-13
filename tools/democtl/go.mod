module soteria/tools/democtl

go 1.26.0

require (
	github.com/google/uuid v1.6.0
	github.com/joho/godotenv v1.5.1
	github.com/rabbitmq/amqp091-go v1.14.0
	soteria/libs/core v0.0.0
	soteria/libs/shopify v0.0.0
)

replace (
	soteria/libs/core => ../../libs/core
	soteria/libs/shopify => ../../libs/shopify
)
