#pragma once

#include <juce_core/juce_core.h>

/**
 * Минимальный HTTP клиент для общения с локальным Node.js сервисом.
 * Все запросы синхронные, вызываются из фонового Timer потока.
 */
class HttpClient
{
public:
    struct Response
    {
        bool   success { false };
        int    statusCode { 0 };
        juce::String body;
    };

    explicit HttpClient(int port) : baseUrl("http://127.0.0.1:" + juce::String(port)) {}

    Response get(const juce::String& endpoint)
    {
        auto url = juce::URL(baseUrl + endpoint);
        return fetch(url, {});
    }

    Response post(const juce::String& endpoint,
                  const std::map<juce::String, juce::String>& params)
    {
        // Собираем JSON вручную (без зависимостей)
        juce::String body = "{";
        bool first = true;
        for (auto& [k, v] : params)
        {
            if (!first) body += ",";
            body += "\"" + k + "\":\"" + v + "\"";
            first = false;
        }
        body += "}";

        auto url = juce::URL(baseUrl + endpoint)
                       .withPOSTData(body);

        return fetch(url, "application/json");
    }

private:
    Response fetch(const juce::URL& url, const juce::String& contentType)
    {
        Response r;

        juce::StringPairArray headers;
        if (contentType.isNotEmpty())
            headers.set("Content-Type", contentType);

        // Таймаут 1 секунда — не блокируем DAW поток
        auto stream = url.createInputStream(
            juce::URL::InputStreamOptions(juce::URL::ParameterHandling::inPostData)
                .withExtraHeaders("Content-Type: " + contentType)
                .withConnectionTimeoutMs(1000)
                .withNumRedirectsToFollow(0)
                .withHttpRequestCmd(contentType.isEmpty() ? "GET" : "POST")
        );

        if (stream != nullptr)
        {
            r.statusCode = 200; // JUCE не даёт HTTP статус напрямую без curl
            r.body = stream->readEntireStreamAsString();
            r.success = r.body.isNotEmpty();
        }

        return r;
    }

    juce::String baseUrl;
};
