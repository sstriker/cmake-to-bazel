cmake_minimum_required(VERSION 3.20)
project(L46 LANGUAGES C VERSION 0.1.0)

find_package(L31 CONFIG REQUIRED)
find_package(L36 CONFIG REQUIRED)
find_package(L38 CONFIG REQUIRED)

add_library(L46 STATIC src/L46.c)
target_include_directories(L46 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L46 PUBLIC L31::L31 L36::L36 L38::L38)

install(TARGETS L46
        EXPORT L46Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L46Targets
        FILE L46Targets.cmake
        NAMESPACE L46::
        DESTINATION lib/cmake/L46)
