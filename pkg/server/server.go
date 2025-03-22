package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apigw/pkg/config"
)

// Ключ контекста для хранения request_id
type contextKey string

const requestIDKey contextKey = "requestID"

// NewsItem представляет краткую информацию о новости (без описания)
type NewsItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	PubDate   string `json:"pub_date"`
	SourceURL string `json:"source_url"`
}

// FullNewsItem представляет полную информацию о новости (с описанием)
type FullNewsItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	PubDate     string `json:"pub_date"`
	SourceURL   string `json:"source_url"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// Comment представляет информацию о комментарии к новости
type Comment struct {
	ID        int64  `json:"id"`
	NewsID    int64  `json:"news_id"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// CommentResponse представляет ответ со списком комментариев
type CommentResponse struct {
	Comments []Comment `json:"comments"`
	NewsID   int64     `json:"news_id"`
}

// PaginatedResponse представляет ответ с пагинацией
type PaginatedResponse struct {
	Items        interface{} `json:"items"`          // Содержимое (новости)
	TotalPages   int         `json:"total_pages"`    // Всего страниц
	CurrentPage  int         `json:"current_page"`   // Текущая страница
	ItemsPerPage int         `json:"items_per_page"` // Элементов на страницу
	TotalItems   int         `json:"total_items"`    // Всего элементов
}

type Server struct {
	config *config.Config
	mux    *http.ServeMux
}

// responseWriter - обертка над http.ResponseWriter для захвата статуса ответа
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader перехватывает статус-код ответа
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func NewServer(cfg *config.Config) *Server {
	srv := &Server{
		config: cfg,
		mux:    http.NewServeMux(),
	}
	srv.setupRoutes()
	return srv
}

func (s *Server) setupRoutes() {
	// Маршруты с применением обоих middleware
	// Порядок важен: requestIDMiddleware должен выполняться первым
	// В Go, внутренний middleware (ближайший к обработчику) выполняется первым,
	// затем выполняется middleware, который его обернул
	s.mux.Handle("/api/news", s.requestIDMiddleware(s.loggingMiddleware(http.HandlerFunc(s.handleNews))))
	s.mux.Handle("/api/fullnews", s.requestIDMiddleware(s.loggingMiddleware(http.HandlerFunc(s.handleFullNews))))

	// Маршруты для комментариев
	s.mux.Handle("/api/comments", s.requestIDMiddleware(s.loggingMiddleware(http.HandlerFunc(s.handleComments))))
	// Новый маршрут для добавления комментариев через POST
	s.mux.Handle("/api/comments/add", s.requestIDMiddleware(s.loggingMiddleware(http.HandlerFunc(s.handleAddComment))))

	// REST-стиль URL для работы с комментариями (принимает ID новости в пути)
	s.mux.Handle("/api/news/", s.requestIDMiddleware(s.loggingMiddleware(http.HandlerFunc(s.handleNewsWithID))))
}

// Middleware для обработки request_id
func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Получаем request_id из query-параметров
		requestID := r.URL.Query().Get("request_id")

		// Если request_id не передан, генерируем его
		if requestID == "" {
			var err error
			requestID, err = generateRequestID(8) // Генерируем строку из 8 символов
			if err != nil {
				log.Printf("Ошибка при генерации request_id: %v", err)
				http.Error(w, "Внутренняя ошибка сервера", http.StatusInternalServerError)
				return
			}
			log.Printf("Сгенерирован новый request_id: %s", requestID)
		} else {
			log.Printf("Получен request_id из параметров: %s", requestID)
		}

		// Добавляем request_id в заголовок ответа для отладки
		w.Header().Set("X-Request-ID", requestID)

		// Добавляем request_id в контекст запроса
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)

		// Проверяем, что request_id успешно добавлен в контекст
		checkID, ok := ctx.Value(requestIDKey).(string)
		if !ok || checkID == "" {
			log.Printf("ОШИБКА: request_id не добавлен в контекст")
		} else {
			log.Printf("request_id успешно добавлен в контекст: %s", checkID)
		}

		// Вызываем следующий обработчик с обновленным контекстом
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMiddleware логирует информацию о запросе после его обработки
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Создаем обертку, чтобы перехватить статус-код ответа
		rw := &responseWriter{w, http.StatusOK}

		// Получаем request_id из контекста
		requestID := "unknown"
		if id, ok := r.Context().Value(requestIDKey).(string); ok && id != "" {
			requestID = id
			log.Printf("loggingMiddleware: получен request_id из контекста: %s", id)
		} else {
			log.Printf("loggingMiddleware: request_id не найден в контексте")

			// Попробуем получить его из заголовка, который должен был установить requestIDMiddleware
			headerID := w.Header().Get("X-Request-ID")
			if headerID != "" {
				log.Printf("loggingMiddleware: нашли request_id в заголовке: %s", headerID)
				requestID = headerID
			}
		}

		// Получаем IP-адрес запроса
		ipAddress := r.RemoteAddr
		// Проверяем X-Forwarded-For заголовок, который может содержать реальный IP за прокси
		if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
			// Берем первый IP из списка (клиентский)
			ips := strings.Split(forwardedFor, ",")
			if len(ips) > 0 {
				ipAddress = strings.TrimSpace(ips[0])
			}
		}

		// Время начала обработки запроса
		start := time.Now()

		// Вызываем следующий обработчик с нашей оберткой вместо оригинального ResponseWriter
		next.ServeHTTP(rw, r)

		// Время завершения обработки запроса
		duration := time.Since(start)

		// Логируем информацию после обработки запроса
		log.Printf(
			"[%s] Request: %s %s | IP: %s | Status: %d | Duration: %v | ID: %s",
			time.Now().Format(time.RFC3339),
			r.Method,
			r.URL.Path,
			ipAddress,
			rw.statusCode,
			duration,
			requestID,
		)
	})
}

// Функция для генерации случайного request_id
func generateRequestID(length int) (string, error) {
	bytes := make([]byte, length/2) // Каждый байт кодируется 2 hex-символами
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.config.Server.Port)
	log.Printf("API Gateway доступен по адресу http://localhost:%d", s.config.Server.Port)
	return http.ListenAndServe(addr, s.mux)
}

// Модифицируем функцию запроса к backend-сервису для передачи request_id
func (s *Server) makeBackendRequest(method, url string, ctx context.Context, body io.Reader) (*http.Response, error) {
	// Создаем новый запрос
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	// Если запрос POST с формой, устанавливаем соответствующий заголовок
	if method == http.MethodPost && body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	// Получаем request_id из контекста
	requestID, ok := ctx.Value(requestIDKey).(string)
	if ok && requestID != "" {
		// Добавляем request_id в параметры запроса к backend-сервису
		q := req.URL.Query()
		q.Add("request_id", requestID)
		req.URL.RawQuery = q.Encode()
	}

	// Выполняем запрос с использованием http.DefaultClient
	return http.DefaultClient.Do(req)
}

// handleNews обрабатывает запросы на получение списка новостей без описания
func (s *Server) handleNews(w http.ResponseWriter, r *http.Request) {
	// Проверяем параметр comm - только для получения новости с комментариями
	query := r.URL.Query()
	commentNewsID := query.Get("comm")

	// Если указан параметр comm - получаем новость и комментарии к ней
	if commentNewsID != "" {
		// Если параметр m присутствует, сообщаем об ошибке - устаревший метод
		if query.Get("m") != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Добавление комментариев через GET-запрос устарело. Используйте POST-запрос на /api/comments/add"})
			return
		}

		// Получаем новость и комментарии к ней
		log.Printf("Получение новости ID: %s с комментариями", commentNewsID)

		// Формируем URL для получения новости
		newsID, err := strconv.ParseInt(commentNewsID, 10, 64)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Некорректный ID новости"})
			return
		}

		// Получаем одну новость с сервиса новостей
		newsURL := fmt.Sprintf("%s/api/news/%d", s.config.Services.News.URL, newsID)
		newsResp, err := s.makeBackendRequest(http.MethodGet, newsURL, r.Context(), nil)
		if err != nil {
			log.Printf("Ошибка при получении новости: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось получить новость"})
			return
		}
		defer newsResp.Body.Close()

		// Проверяем статус ответа от сервиса новостей
		if newsResp.StatusCode != http.StatusOK {
			log.Printf("Сервис новостей вернул статус: %d", newsResp.StatusCode)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(newsResp.StatusCode)
			json.NewEncoder(w).Encode(map[string]string{"error": "Новость не найдена"})
			return
		}

		// Читаем ответ от сервиса новостей
		newsBody, err := io.ReadAll(newsResp.Body)
		if err != nil {
			log.Printf("Ошибка при чтении ответа: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке ответа от сервиса новостей"})
			return
		}

		// Декодируем новость - сервис возвращает массив с одним элементом
		var newsItems []map[string]interface{}
		if err := json.Unmarshal(newsBody, &newsItems); err != nil {
			log.Printf("Ошибка при декодировании новости: %v, тело: %s", err, string(newsBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке новости"})
			return
		}

		// Проверяем, что в массиве есть хотя бы один элемент
		if len(newsItems) == 0 {
			log.Printf("Новость не найдена")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "Новость не найдена"})
			return
		}

		// Берем первую новость из массива
		newsItem := newsItems[0]

		// Получаем комментарии к новости
		commURL := fmt.Sprintf("%s/api/comm_news?id=%d", s.config.Services.Comments.URL, newsID)
		commResp, err := s.makeBackendRequest(http.MethodGet, commURL, r.Context(), nil)
		if err != nil {
			log.Printf("Ошибка при получении комментариев: %v", err)
			// В случае ошибки, возвращаем только новость без комментариев
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"news":     newsItem,
				"comments": []interface{}{},
			})
			return
		}
		defer commResp.Body.Close()

		// Читаем ответ от сервиса комментариев
		commBody, err := io.ReadAll(commResp.Body)
		if err != nil {
			log.Printf("Ошибка при чтении ответа комментариев: %v", err)
			// В случае ошибки, возвращаем только новость без комментариев
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"news":     newsItem,
				"comments": []interface{}{},
			})
			return
		}

		// Декодируем комментарии
		var commResponse []interface{}
		if err := json.Unmarshal(commBody, &commResponse); err != nil {
			log.Printf("Ошибка при декодировании комментариев: %v, тело: %s", err, string(commBody))
			// В случае ошибки, возвращаем только новость без комментариев
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"news":     newsItem,
				"comments": []interface{}{},
			})
			return
		}

		// Формируем и отправляем ответ с новостью и комментариями
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"news":     newsItem,
			"comments": commResponse,
		})
		return
	}

	// Если не указан параметр comm, обрабатываем как обычный запрос новостей
	// Обрабатываем только GET запросы
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не разрешен", http.StatusMethodNotAllowed)
		return
	}

	// Получаем и обрабатываем параметры запроса
	pageStr := query.Get("page")
	countStr := query.Get("count")
	searchTerm := query.Get("s")

	// Параметры пагинации по умолчанию
	page := 1
	count := 10

	// Парсим параметр страницы
	if pageStr != "" {
		parsedPage, err := strconv.Atoi(pageStr)
		if err == nil && parsedPage > 0 {
			page = parsedPage
		}
	}

	// Парсим параметр количества элементов на страницу
	if countStr != "" {
		parsedCount, err := strconv.Atoi(countStr)
		if err == nil && parsedCount > 0 {
			count = parsedCount
		}
	}

	// Формируем URL для сервиса новостей - без указания количества, получим все новости
	newsURL := fmt.Sprintf("%s/api/news/", s.config.Services.News.URL)

	// Используем модифицированную функцию для запроса к backend, передавая context с request_id
	resp, err := s.makeBackendRequest(http.MethodGet, newsURL, r.Context(), nil)
	if err != nil {
		log.Printf("Ошибка при получении новостей: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось получить новости"})
		return
	}
	defer resp.Body.Close()

	// Устанавливаем тип содержимого JSON для всех ответов
	w.Header().Set("Content-Type", "application/json")

	if resp.StatusCode != http.StatusOK {
		log.Printf("Бэкенд вернул статус: %d", resp.StatusCode)
		sendEmptyPaginatedResponse(w, page, count)
		return
	}

	// Читаем тело ответа
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Ошибка при чтении ответа: %v", err)
		sendEmptyPaginatedResponse(w, page, count)
		return
	}

	// Обрабатываем пустой ответ
	if len(body) == 0 {
		sendEmptyPaginatedResponse(w, page, count)
		return
	}

	// Декодируем полные новости из бэкенда
	var allNews []map[string]interface{}
	if err := json.Unmarshal(body, &allNews); err != nil {
		log.Printf("Ошибка при декодировании новостей: %v", err)
		sendEmptyPaginatedResponse(w, page, count)
		return
	}

	// Фильтруем новости по поисковому запросу, если он указан
	var filteredNews []map[string]interface{}
	if searchTerm != "" {
		searchTerm = strings.ToLower(searchTerm)
		for _, item := range allNews {
			title, ok := item["title"].(string)
			if !ok {
				continue
			}

			if strings.Contains(strings.ToLower(title), searchTerm) {
				filteredNews = append(filteredNews, item)
			}
		}
	} else {
		filteredNews = allNews
	}

	// Применяем пагинацию к отфильтрованным новостям
	totalItems := len(filteredNews)
	totalPages := (totalItems + count - 1) / count // Округление вверх

	// Проверяем, что запрошенная страница существует
	if totalItems == 0 {
		sendEmptyPaginatedResponse(w, page, count)
		return
	}

	// Вычисляем индексы для текущей страницы согласно требованиям
	// Для page=2, count=5 должны получить элементы с 10 по 15
	// т.е. для page=2 начинаем с индекса 5*2=10-1=9
	startIndex := (page - 1) * count
	endIndex := startIndex + count

	// Проверяем валидность индексов
	if startIndex >= totalItems {
		// Запрошенная страница выходит за пределы доступных данных
		sendEmptyPaginatedResponse(w, page, count)
		return
	}

	// Обрезаем endIndex, если он выходит за пределы массива
	if endIndex > totalItems {
		endIndex = totalItems
	}

	// Получаем новости для текущей страницы
	pagedNews := filteredNews[startIndex:endIndex]

	// Конвертируем полные новости в краткий формат
	news := make([]NewsItem, 0, len(pagedNews))
	for _, item := range pagedNews {
		id, ok := item["id"].(float64)
		if !ok {
			continue
		}

		newsItem := NewsItem{
			ID:        int64(id),
			Title:     getStringValue(item, "title"),
			PubDate:   getStringValue(item, "pub_date"),
			SourceURL: getStringValue(item, "source_url"),
		}
		news = append(news, newsItem)
	}

	// Формируем и отправляем ответ с пагинацией
	response := PaginatedResponse{
		Items:        news,
		TotalPages:   totalPages,
		CurrentPage:  page,
		ItemsPerPage: count,
		TotalItems:   totalItems,
	}

	json.NewEncoder(w).Encode(response)
}

// handleFullNews обрабатывает запросы на получение полных новостей с описанием
func (s *Server) handleFullNews(w http.ResponseWriter, r *http.Request) {
	// Только GET запросы
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не разрешен", http.StatusMethodNotAllowed)
		return
	}

	// Получаем и обрабатываем параметры запроса
	query := r.URL.Query()
	pageStr := query.Get("page")
	countStr := query.Get("count")
	searchTerm := query.Get("s")

	// Параметры пагинации по умолчанию
	page := 1
	count := 10

	// Парсим параметр страницы
	if pageStr != "" {
		parsedPage, err := strconv.Atoi(pageStr)
		if err == nil && parsedPage > 0 {
			page = parsedPage
		}
	}

	// Парсим параметр количества элементов на страницу
	if countStr != "" {
		parsedCount, err := strconv.Atoi(countStr)
		if err == nil && parsedCount > 0 {
			count = parsedCount
		}
	}

	// Формируем URL для сервиса новостей - без указания количества, получим все новости
	newsURL := fmt.Sprintf("%s/api/news/", s.config.Services.News.URL)

	// Используем модифицированную функцию для запроса к backend, передавая context с request_id
	resp, err := s.makeBackendRequest(http.MethodGet, newsURL, r.Context(), nil)
	if err != nil {
		log.Printf("Ошибка при получении новостей: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось получить новости"})
		return
	}
	defer resp.Body.Close()

	// Устанавливаем тип содержимого JSON для всех ответов
	w.Header().Set("Content-Type", "application/json")

	if resp.StatusCode != http.StatusOK {
		log.Printf("Бэкенд вернул статус: %d", resp.StatusCode)
		sendEmptyPaginatedResponseFull(w, page, count)
		return
	}

	// Читаем тело ответа
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Ошибка при чтении ответа: %v", err)
		sendEmptyPaginatedResponseFull(w, page, count)
		return
	}

	// Обрабатываем пустой ответ
	if len(body) == 0 {
		sendEmptyPaginatedResponseFull(w, page, count)
		return
	}

	// Декодируем полные новости из бэкенда
	var allNews []map[string]interface{}
	if err := json.Unmarshal(body, &allNews); err != nil {
		log.Printf("Ошибка при декодировании новостей: %v", err)
		sendEmptyPaginatedResponseFull(w, page, count)
		return
	}

	// Фильтруем новости по поисковому запросу, если он указан
	var filteredNews []map[string]interface{}
	if searchTerm != "" {
		searchTerm = strings.ToLower(searchTerm)
		for _, item := range allNews {
			title, ok := item["title"].(string)
			if !ok {
				continue
			}

			if strings.Contains(strings.ToLower(title), searchTerm) {
				filteredNews = append(filteredNews, item)
			}
		}
	} else {
		filteredNews = allNews
	}

	// Применяем пагинацию к отфильтрованным новостям
	totalItems := len(filteredNews)
	totalPages := (totalItems + count - 1) / count // Округление вверх

	// Проверяем, что запрошенная страница существует
	if totalItems == 0 {
		sendEmptyPaginatedResponseFull(w, page, count)
		return
	}

	// Вычисляем индексы для текущей страницы согласно требованиям
	// Для page=2, count=5 должны получить элементы с 10 по 15
	// т.е. для page=2 начинаем с индекса 5*2=10-1=9
	startIndex := (page - 1) * count
	endIndex := startIndex + count

	// Проверяем валидность индексов
	if startIndex >= totalItems {
		// Запрошенная страница выходит за пределы доступных данных
		sendEmptyPaginatedResponseFull(w, page, count)
		return
	}

	// Обрезаем endIndex, если он выходит за пределы массива
	if endIndex > totalItems {
		endIndex = totalItems
	}

	// Получаем новости для текущей страницы
	pagedNews := filteredNews[startIndex:endIndex]

	// Конвертируем в полный формат новостей
	fullNews := make([]FullNewsItem, 0, len(pagedNews))
	for _, item := range pagedNews {
		id, ok := item["id"].(float64)
		if !ok {
			continue
		}

		fullNewsItem := FullNewsItem{
			ID:          int64(id),
			Title:       getStringValue(item, "title"),
			Description: getStringValue(item, "description"),
			PubDate:     getStringValue(item, "pub_date"),
			SourceURL:   getStringValue(item, "source_url"),
		}

		// Добавляем created_at, если имеется
		if createdAt, ok := item["created_at"].(string); ok {
			fullNewsItem.CreatedAt = createdAt
		}

		fullNews = append(fullNews, fullNewsItem)
	}

	// Формируем и отправляем ответ с пагинацией
	response := PaginatedResponse{
		Items:        fullNews,
		TotalPages:   totalPages,
		CurrentPage:  page,
		ItemsPerPage: count,
		TotalItems:   totalItems,
	}

	json.NewEncoder(w).Encode(response)
}

// handleAddComment обрабатывает запросы на добавление комментария к новости через POST запрос
func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	// Проверяем, что запрос POST
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не разрешен. Используйте POST", http.StatusMethodNotAllowed)
		return
	}

	// Устанавливаем тип содержимого JSON для всех ответов
	w.Header().Set("Content-Type", "application/json")

	// Логируем заголовки запроса для диагностики
	log.Printf("Получен запрос на добавление комментария. Headers: %v", r.Header)

	// Получение ID новости из URL параметров
	newsIDStr := r.URL.Query().Get("news_id")
	if newsIDStr == "" {
		newsIDStr = r.URL.Query().Get("id")
	}
	log.Printf("ID новости из URL параметров: %s", newsIDStr)

	// Проверяем, что newsID это число
	newsID, err := strconv.ParseInt(newsIDStr, 10, 64)
	if err != nil || newsIDStr == "" {
		log.Printf("Некорректный ID новости: '%s', ошибка: %v", newsIDStr, err)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Некорректный ID новости. Укажите числовой ID в параметре news_id или id."})
		return
	}

	// Чтение JSON-данных из тела запроса
	var requestData struct {
		Text string `json:"text"`
	}

	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		log.Printf("Ошибка при чтении JSON: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Неверный формат JSON или отсутствие тела запроса"})
		return
	}
	defer r.Body.Close()

	// Логируем полученные данные
	log.Printf("Получен текст комментария: %s", requestData.Text)

	// Проверяем, что комментарий не пустой
	if requestData.Text == "" {
		log.Printf("Получен пустой комментарий")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Комментарий не может быть пустым. Укажите текст в поле text."})
		return
	}

	// Формируем URL для сервиса комментариев
	commURL := fmt.Sprintf("%s/api/comm_add_news?id=%d", s.config.Services.Comments.URL, newsID)
	log.Printf("Отправка запроса на URL: %s", commURL)

	// Пересылаем JSON как есть на сервис комментариев
	jsonData := map[string]string{"text": requestData.Text}
	jsonBody, err := json.Marshal(jsonData)
	if err != nil {
		log.Printf("Ошибка при создании JSON: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке запроса"})
		return
	}

	// Логируем тело запроса
	log.Printf("Тело запроса: %s", string(jsonBody))

	// Создаем новый запрос с JSON-телом
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, commURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("Ошибка при создании запроса: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при создании запроса к сервису комментариев"})
		return
	}

	// Устанавливаем заголовок Content-Type для JSON
	req.Header.Set("Content-Type", "application/json")

	// Получаем request_id из контекста и добавляем в URL
	if requestID, ok := r.Context().Value(requestIDKey).(string); ok && requestID != "" {
		q := req.URL.Query()
		q.Add("request_id", requestID)
		req.URL.RawQuery = q.Encode()
	}

	// Отправляем запрос
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Ошибка при добавлении комментария: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось добавить комментарий: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Проверяем статус ответа
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("Сервис комментариев вернул статус: %d, тело: %s", resp.StatusCode, string(respBody))
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при добавлении комментария"})
		return
	}

	// Читаем ответ от сервиса комментариев
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Ошибка при чтении ответа: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке ответа от сервиса комментариев"})
		return
	}

	// Логируем успешный ответ
	log.Printf("Комментарий успешно добавлен: %s", string(respBody))

	// Устанавливаем тип содержимого JSON для ответа
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}

// handleComments переименован в handleComments для соответствия конвенции других обработчиков
func (s *Server) handleComments(w http.ResponseWriter, r *http.Request) {
	// Только GET запросы
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не разрешен", http.StatusMethodNotAllowed)
		return
	}

	// Устанавливаем тип содержимого JSON для всех ответов
	w.Header().Set("Content-Type", "application/json")

	// Получаем ID новости из параметров запроса
	newsIDStr := r.URL.Query().Get("id")
	if newsIDStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Не указан ID новости"})
		return
	}

	// Проверяем, что newsID это число
	newsID, err := strconv.ParseInt(newsIDStr, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Некорректный ID новости"})
		return
	}

	// Формируем URL для получения комментариев от сервиса комментариев
	commURL := fmt.Sprintf("%s/api/comm_news?id=%d", s.config.Services.Comments.URL, newsID)
	log.Printf("Отправка запроса на сервис комментариев: %s", commURL)

	// Отправляем GET запрос к сервису комментариев
	resp, err := s.makeBackendRequest(http.MethodGet, commURL, r.Context(), nil)
	if err != nil {
		log.Printf("Ошибка при получении комментариев: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось получить комментарии: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Проверяем статус ответа
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("Сервис комментариев вернул статус: %d, тело: %s", resp.StatusCode, string(respBody))
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при получении комментариев"})
		return
	}

	// Читаем ответ от сервиса комментариев
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Ошибка при чтении ответа от сервиса комментариев: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке комментариев"})
		return
	}

	// Проверяем, что ответ от сервиса комментариев является валидным JSON
	var commResp any
	if err := json.Unmarshal(body, &commResp); err != nil {
		log.Printf("Ошибка при разборе JSON: %v, тело: %s", err, string(body))
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке комментариев"})
		return
	}

	// Передаем ответ в исходном виде клиенту
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// Вспомогательная функция для возврата пустого пагинированного ответа для NewsItem
func sendEmptyPaginatedResponse(w http.ResponseWriter, page, count int) {
	response := PaginatedResponse{
		Items:        []NewsItem{},
		TotalPages:   0,
		CurrentPage:  page,
		ItemsPerPage: count,
		TotalItems:   0,
	}
	json.NewEncoder(w).Encode(response)
}

// Вспомогательная функция для возврата пустого пагинированного ответа для FullNewsItem
func sendEmptyPaginatedResponseFull(w http.ResponseWriter, page, count int) {
	response := PaginatedResponse{
		Items:        []FullNewsItem{},
		TotalPages:   0,
		CurrentPage:  page,
		ItemsPerPage: count,
		TotalItems:   0,
	}
	json.NewEncoder(w).Encode(response)
}

// Вспомогательная функция для безопасного получения строковых значений из карты
func getStringValue(item map[string]interface{}, key string) string {
	if value, ok := item[key].(string); ok {
		return value
	}
	return ""
}

// handleNewsWithID обрабатывает запросы на получение новости по её ID
func (s *Server) handleNewsWithID(w http.ResponseWriter, r *http.Request) {
	// Получаем ID новости из пути запроса
	newsIDStr := strings.TrimPrefix(r.URL.Path, "/api/news/")
	newsID, err := strconv.ParseInt(newsIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Некорректный ID новости", http.StatusBadRequest)
		return
	}

	// Получаем новость с сервиса новостей
	newsURL := fmt.Sprintf("%s/api/news/%d", s.config.Services.News.URL, newsID)
	newsResp, err := s.makeBackendRequest(http.MethodGet, newsURL, r.Context(), nil)
	if err != nil {
		log.Printf("Ошибка при получении новости: %v", err)
		http.Error(w, "Не удалось получить новость", http.StatusInternalServerError)
		return
	}
	defer newsResp.Body.Close()

	// Проверяем статус ответа от сервиса новостей
	if newsResp.StatusCode != http.StatusOK {
		log.Printf("Сервис новостей вернул статус: %d", newsResp.StatusCode)
		http.Error(w, "Новость не найдена", newsResp.StatusCode)
		return
	}

	// Читаем ответ от сервиса новостей
	newsBody, err := io.ReadAll(newsResp.Body)
	if err != nil {
		log.Printf("Ошибка при чтении ответа: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке ответа от сервиса новостей"})
		return
	}

	// Декодируем новость - сервис возвращает массив с одним элементом
	var newsItems []map[string]interface{}
	if err := json.Unmarshal(newsBody, &newsItems); err != nil {
		log.Printf("Ошибка при декодировании новости: %v, тело: %s", err, string(newsBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Ошибка при обработке новости"})
		return
	}

	// Проверяем, что в массиве есть хотя бы один элемент
	if len(newsItems) == 0 {
		log.Printf("Новость не найдена")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Новость не найдена"})
		return
	}

	// Берем первую новость из массива
	newsItem := newsItems[0]

	// Отправляем новость клиенту
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(newsItem)
}
