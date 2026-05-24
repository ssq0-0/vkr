package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/streaming-system/gateway/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// ProcessorClient представляет клиент для связи с Processor Service
type ProcessorClient struct {
	conn   *grpc.ClientConn
	client pb.StreamingProcessorClient
	addr   string
	mu     sync.Mutex
}

// NewProcessorClient создает новый клиент для Processor Service
func NewProcessorClient(addr string) (*ProcessorClient, error) {
	conn, err := grpc.Dial(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(100*1024*1024), // 100 MB
			grpc.MaxCallSendMsgSize(100*1024*1024),
		),
	)
	if err != nil {
		return nil, err
	}

	return &ProcessorClient{
		conn:   conn,
		client: pb.NewStreamingProcessorClient(conn),
		addr:   addr,
	}, nil
}

// Close закрывает соединение
func (c *ProcessorClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ProcessStream обрабатывает поток медиа-чанков
func (c *ProcessorClient) ProcessStream(ctx context.Context, chunks <-chan *pb.MediaChunk, memoryMode string) (<-chan *pb.ProcessedChunk, <-chan error) {
	results := make(chan *pb.ProcessedChunk, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(results)
		defer close(errs)

		ctx = metadata.AppendToOutgoingContext(ctx, "memory-mode", normalizeMemoryMode(memoryMode))
		stream, err := c.client.ProcessMediaStream(ctx)
		if err != nil {
			errs <- err
			return
		}

		// Горутина для отправки чанков
		sendErr := make(chan error, 1)
		go func() {
			for chunk := range chunks {
				if err := stream.Send(chunk); err != nil {
					sendErr <- err
					return
				}
			}
			stream.CloseSend()
			sendErr <- nil
		}()

		// Читаем результаты
		for {
			result, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				errs <- err
				return
			}

			select {
			case results <- result:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}

		// Проверяем ошибку отправки
		if err := <-sendErr; err != nil {
			errs <- err
		}
	}()

	return results, errs
}

// ProcessSingleChunk обрабатывает один чанк
func (c *ProcessorClient) ProcessSingleChunk(ctx context.Context, chunk *pb.MediaChunk, memoryMode string) (*pb.ProcessedChunk, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "memory-mode", normalizeMemoryMode(memoryMode))
	stream, err := c.client.ProcessMediaStream(ctx)
	if err != nil {
		return nil, err
	}

	if err := stream.Send(chunk); err != nil {
		return nil, err
	}

	if err := stream.CloseSend(); err != nil {
		return nil, err
	}

	result, err := stream.Recv()
	if err != nil {
		return nil, err
	}

	return result, nil
}

func normalizeMemoryMode(mode string) string {
	switch mode {
	case "heap", "no_arena":
		return "no_arena"
	default:
		return "arena"
	}
}

// GetStats получает статистику процессора
func (c *ProcessorClient) GetStats(ctx context.Context) (*pb.StatsResponse, error) {
	return c.client.GetStats(ctx, &pb.StatsRequest{})
}

// ProcessorPool пул соединений к Processor Service
type ProcessorPool struct {
	clients []*ProcessorClient
	addrs   []string
	current uint64 // round-robin counter
	sticky  int32  // -1 = round-robin; >=0 = весь трафик на clients[sticky]
	mu      sync.RWMutex
}

// NewProcessorPool создает пул соединений
func NewProcessorPool(addrs []string) (*ProcessorPool, error) {
	pool := &ProcessorPool{
		addrs:   addrs,
		clients: make([]*ProcessorClient, 0, len(addrs)),
		sticky:  -1,
	}

	for _, addr := range addrs {
		client, err := NewProcessorClient(addr)
		if err != nil {
			// Закрываем уже созданные соединения
			pool.Close()
			return nil, err
		}
		pool.clients = append(pool.clients, client)
	}

	return pool, nil
}

// Get возвращает клиент из пула: при активном sticky — всегда один и тот же, иначе round-robin.
func (p *ProcessorPool) Get() (*ProcessorClient, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.clients) == 0 {
		return nil, errors.New("no available clients in pool")
	}

	if s := atomic.LoadInt32(&p.sticky); s >= 0 && int(s) < len(p.clients) {
		return p.clients[s], nil
	}

	idx := atomic.AddUint64(&p.current, 1) % uint64(len(p.clients))
	return p.clients[idx], nil
}

// Count число процессоров в пуле.
func (p *ProcessorPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// Addrs копия адресов в порядке индексов (для UI / конфиг).
func (p *ProcessorPool) Addrs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.addrs))
	copy(out, p.addrs)
	return out
}

// ClientAt клиент по индексу (для опроса статистики всех инстансов).
func (p *ProcessorPool) ClientAt(i int) (*ProcessorClient, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if i < 0 || i >= len(p.clients) {
		return nil, errors.New("processor index out of range")
	}
	return p.clients[i], nil
}

// Addr адрес gRPC этого клиента.
func (c *ProcessorClient) Addr() string {
	return c.addr
}

// GetSticky возвращает -1 если round-robin, иначе закреплённый индекс.
func (p *ProcessorPool) GetSticky() int {
	return int(atomic.LoadInt32(&p.sticky))
}

// SetSticky idx < 0 — снять закрепление (round-robin); idx >= 0 — весь трафик на этот индекс.
func (p *ProcessorPool) SetSticky(idx int) error {
	p.mu.RLock()
	n := len(p.clients)
	p.mu.RUnlock()
	if idx >= n {
		return errors.New("processor index out of range")
	}
	if idx < 0 {
		atomic.StoreInt32(&p.sticky, -1)
		return nil
	}
	atomic.StoreInt32(&p.sticky, int32(idx))
	return nil
}

// Close закрывает все соединения в пуле
func (p *ProcessorPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for _, client := range p.clients {
		if err := client.Close(); err != nil {
			lastErr = err
		}
	}
	p.clients = nil
	return lastErr
}

// HealthCheck проверяет здоровье соединений
func (p *ProcessorPool) HealthCheck(ctx context.Context) map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	results := make(map[string]bool)
	for _, client := range p.clients {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := client.GetStats(ctx)
		cancel()
		results[client.addr] = err == nil
	}
	return results
}
