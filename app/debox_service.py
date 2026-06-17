import boxbotapi

from app.config import settings


def send_notification(chat_id: str, chat_type: str, text: str) -> str:
    bot = boxbotapi.NewBotAPI(settings.debox_bot_api_key, settings.debox_bot_api_secret)
    message = boxbotapi.NewMessage(chat_id, chat_type, text)
    message.ParseMode = boxbotapi.ModeHTML
    sent = bot.Send(message)
    return sent.MessageID

