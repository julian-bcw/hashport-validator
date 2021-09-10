module github.com/limechain/hedera-eth-bridge-validator

go 1.13

require (
	github.com/btcsuite/btcd v0.21.0-beta // indirect
	github.com/caarlos0/env/v6 v6.4.0
	github.com/ethereum/go-ethereum v1.10.8
	github.com/go-chi/chi v4.1.2+incompatible
	github.com/go-chi/render v1.0.1
	github.com/golang/protobuf v1.5.2
	github.com/hashgraph/hedera-sdk-go/v2 v2.1.5-beta.3
	github.com/hashgraph/hedera-state-proof-verifier-go v0.0.0-20210331132016-d77f113cf098
	github.com/jackc/pgx/v4 v4.9.2 // indirect
	github.com/pkg/errors v0.9.1
	github.com/rs/cors v1.7.0
	github.com/sirupsen/logrus v1.4.2
	github.com/stretchr/testify v1.7.0
	google.golang.org/genproto v0.0.0-20210406143921-e86de6bf7a46 // indirect
	google.golang.org/grpc v1.37.0 // indirect
	google.golang.org/protobuf v1.26.0
	gopkg.in/yaml.v2 v2.4.0
	gorm.io/driver/postgres v1.0.5
	gorm.io/gorm v1.20.6
)
