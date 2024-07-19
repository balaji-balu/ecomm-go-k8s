package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "time"

    "github.com/go-redis/redis/v8"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "context"
    _ "github.com/go-sql-driver/mysql"
    "hash/fnv"
)

var ctx = context.Background()
var rdb *redis.Client
var dbs []*sql.DB

var (
    dbRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name: "db_request_duration_seconds",
        Help: "Duration of database requests.",
    }, []string{"operation"})

    cacheRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name: "cache_request_duration_seconds",
        Help: "Duration of cache requests.",
    }, []string{"operation"})
)

func init() {
    // Setup Redis
    rdb = redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
        Password: "", // no password set
        DB: 0,  // use default DB
    })

    // Setup MySQL shards
    db1, err := sql.Open("mysql", "user:mysql-root-password@tcp(localhost:3306)/shard1")
    if err != nil {
        log.Fatalf("Error opening database: %v", err)
    }

    db2, err := sql.Open("mysql", "user:mysql-root-password@tcp(localhost:3307)/shard2")
    if err != nil {
        log.Fatalf("Error opening database: %v", err)
    }

    dbs = []*sql.DB{db1, db2}

    prometheus.MustRegister(dbRequestDuration)
    prometheus.MustRegister(cacheRequestDuration)

    // Setup log file
    logFile, err := os.OpenFile("/path/to/your/logfile.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
    if err != nil {
        log.Fatalf("Error opening log file: %v", err)
    }
    log.SetOutput(logFile)
}

func getShard(key string) *sql.DB {
    hash := fnv.New32a()
    hash.Write([]byte(key))
    return dbs[hash.Sum32()%uint32(len(dbs))]
}

func loggingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.RequestURI, time.Since(start))
    })
}

func metricsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.RequestURI, time.Since(start))
    })
}

func instrumentDB(operation string, fn func() error) error {
    start := time.Now()
    err := fn()
    duration := time.Since(start).Seconds()
    dbRequestDuration.WithLabelValues(operation).Observe(duration)
    return err
}

func instrumentCache(operation string, fn func() (string, error)) (string, error) {
    start := time.Now()
    result, err := fn()
    duration := time.Since(start).Seconds()
    cacheRequestDuration.WithLabelValues(operation).Observe(duration)
    return result, err
}

func writeError(w http.ResponseWriter, status int, message string, err error) {
    log.Printf("Error: %v", err)
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func getHandler(w http.ResponseWriter, r *http.Request) {
    key := r.URL.Query().Get("key")
    value, err := getFromCache(key)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "Error fetching from cache", err)
        return
    }

    if value == "" {
        value, err = getFromDB(key)
        if err != nil {
            writeError(w, http.StatusInternalServerError, "Error fetching from DB", err)
            return
        }
        setToCache(key, value)
    }

    w.WriteHeader(http.StatusOK)
    w.Write([]byte(value))
}

func setHandler(w http.ResponseWriter, r *http.Request) {
    key := r.URL.Query().Get("key")
    value := r.URL.Query().Get("value")
    err := setToDB(key, value)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "Error saving to DB", err)
        return
    }

    setToCache(key, value)
    w.WriteHeader(http.StatusOK)
    w.Write([]byte("Success"))
}

func getFromCache(key string) (string, error) {
    return instrumentCache("get", func() (string, error) {
        value, err := rdb.Get(ctx, key).Result()
        if err == redis.Nil {
            return "", nil // key does not exist
        } else if err != nil {
            return "", err
        }
        return value, nil
    })
}

func setToCache(key string, value string) error {
    return instrumentCache("set", func() (string, error) {
        err := rdb.Set(ctx, key, value, 10*time.Minute).Err()
        if err != nil {
            return "", err
        }
        return "", nil
    })
}

func getFromDB(key string) (string, error) {
    var value string
    err := instrumentDB("get", func() error {
        db := getShard(key)
        return db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&value)
    })
    if err != nil {
        return "", err
    }
    return value, nil
}

func setToDB(key string, value string) error {
    return instrumentDB("set", func() error {
        db := getShard(key)
        _, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = ?", key, value, value)
        return err
    })
}

func productsHandler(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case "GET":
        listProducts(w, r)
    case "POST":
        createProduct(w, r)
    default:
        w.WriteHeader(http.StatusMethodNotAllowed)
    }
}

func listProducts(w http.ResponseWriter, r *http.Request) {
    // Fetch and return a list of products
    products := []map[string]interface{}{
        {"id": 1, "name": "Product 1", "price": 100.0},
        {"id": 2, "name": "Product 2", "price": 200.0},
    }
    json.NewEncoder(w).Encode(products)
}

func createProduct(w http.ResponseWriter, r *http.Request) {
    var product struct {
        Name  string  `json:"name"`
        Price float64 `json:"price"`
    }
    if err := json.NewDecoder(r.Body).Decode(&product); err != nil {
        writeError(w, http.StatusBadRequest, "Invalid request payload", err)
        return
    }
    db := getShard(product.Name)
    _, err := db.Exec("INSERT INTO products (name, price) VALUES (?, ?)",
        product.Name, product.Price)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "Error inserting product", err)
        return
    }
    w.WriteHeader(http.StatusCreated)
}

func usersHandler(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case "GET":
        listUsers(w, r)
    case "POST":
        createUser(w, r)
    default:
        w.WriteHeader(http.StatusMethodNotAllowed)
    }
}

func listUsers(w http.ResponseWriter, r *http.Request) {
    // Fetch and return a list of users
    users := []map[string]interface{}{
        {"id": 1, "name": "User 1", "email": "user1@example.com"},
        {"id": 2, "name": "User 2", "email": "user2@example.com"},
    }
    json.NewEncoder(w).Encode(users)
}

func createUser(w http.ResponseWriter, r *http.Request) {
    var user struct {
        Name  string `json:"name"`
        Email string `json:"email"`
    }
    if err := json.NewDecoder(r.Body).Decode(&user); err != nil {
        writeError(w, http.StatusBadRequest, "Invalid request payload", err)
        return
    }
    db := getShard(user.Email)
    _, err := db.Exec("INSERT INTO users (name, email) VALUES (?, ?)",
        user.Name, user.Email)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "Error inserting user", err)
        return
    }
    w.WriteHeader(http.StatusCreated)
}

func ordersHandler(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case "GET":
        listOrders(w, r)
    case "POST":
        createOrder(w, r)
    default:
        w.WriteHeader(http.StatusMethodNotAllowed)
    }
}

func listOrders(w http.ResponseWriter, r *http.Request) {
    // Fetch and return a list of orders
    orders := []map[string]interface{}{
        {"id": 1, "product_id": 1, "user_id": 1, "quantity": 2},
        {"id": 2, "product_id": 2, "user_id": 2, "quantity": 1},
    }
    json.NewEncoder(w).Encode(orders)
}

func createOrder(w http.ResponseWriter, r *http.Request) {
    var order struct {
        ProductID int `json:"product_id"`
        UserID    int `json:"user_id"`
        Quantity  int `json:"quantity"`
    }
    if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
        writeError(w, http.StatusBadRequest, "Invalid request payload", err)
        return
    }
    db := getShard(fmt.Sprintf("%d", order.ProductID))
    _, err := db.Exec("INSERT INTO orders (product_id, user_id, quantity) VALUES (?, ?, ?)",
        order.ProductID, order.UserID, order.Quantity)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "Error inserting order", err)
        return
    }
    w.WriteHeader(http.StatusCreated)
}

func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("/get", getHandler)
    mux.HandleFunc("/set", setHandler)
    mux.HandleFunc("/products", productsHandler)
    mux.HandleFunc("/users", usersHandler)
    mux.HandleFunc("/orders", ordersHandler)
    mux.Handle("/metrics", promhttp.Handler())

    loggedMux := metricsMiddleware(loggingMiddleware(mux))

    fmt.Println("Server started at :8080")
    log.Fatal(http.ListenAndServe(":8080", loggedMux))
}
